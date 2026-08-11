package sqlite

/*
#cgo pkg-config: sqlite3
#include <stdlib.h>
#include <sqlite3.h>

static int bind_text_transient(sqlite3_stmt *stmt, int idx, const char *value, int len) {
    return sqlite3_bind_text(stmt, idx, value, len, SQLITE_TRANSIENT);
}
static int bind_blob_transient(sqlite3_stmt *stmt, int idx, const void *value, int len) {
    return sqlite3_bind_blob(stmt, idx, value, len, SQLITE_TRANSIENT);
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/sid-pani/lessen-mcp-gateway/internal/model"
	"github.com/sid-pani/lessen-mcp-gateway/internal/storage"
)

type Store struct {
	mu     sync.Mutex
	db     *C.sqlite3
	closed bool
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("sqlite path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil && filepath.Dir(path) != "." {
		return nil, err
	}
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var db *C.sqlite3
	flags := C.SQLITE_OPEN_READWRITE | C.SQLITE_OPEN_CREATE | C.SQLITE_OPEN_FULLMUTEX
	if rc := C.sqlite3_open_v2(cpath, &db, C.int(flags), nil); rc != C.SQLITE_OK {
		message := "unable to open sqlite database"
		if db != nil {
			message = C.GoString(C.sqlite3_errmsg(db))
			C.sqlite3_close_v2(db)
		}
		return nil, fmt.Errorf("sqlite: %s", message)
	}
	s := &Store{db: db}
	if rc := C.sqlite3_busy_timeout(db, 5000); rc != C.SQLITE_OK {
		_ = s.Close()
		return nil, s.err(rc)
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA foreign_keys=ON;",
		"PRAGMA temp_store=FILE;",
		"PRAGMA cache_size=-2048;",
	} {
		if err := s.execLocked(pragma); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	if err := s.migrate(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS servers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  transport TEXT NOT NULL,
  status TEXT NOT NULL,
  status_message TEXT NOT NULL DEFAULT '',
  endpoint TEXT NOT NULL DEFAULT '',
  protocol_version TEXT NOT NULL DEFAULT '',
  server_version TEXT NOT NULL DEFAULT '',
  capabilities BLOB,
  tool_count INTEGER NOT NULL DEFAULT 0,
  resource_count INTEGER NOT NULL DEFAULT 0,
  resource_template_count INTEGER NOT NULL DEFAULT 0,
  prompt_count INTEGER NOT NULL DEFAULT 0,
  last_health_check INTEGER NOT NULL DEFAULT 0,
  last_discovered_at INTEGER NOT NULL DEFAULT 0,
  connected_since INTEGER NOT NULL DEFAULT 0,
  config_hash TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS tools (
  server_id TEXT NOT NULL,
  name TEXT NOT NULL,
  exposed_name TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  search_text TEXT NOT NULL DEFAULT '',
  input_schema BLOB,
  output_schema BLOB,
  annotations BLOB,
  schema_hash TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(server_id, name),
  FOREIGN KEY(server_id) REFERENCES servers(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS tools_exposed_name_idx ON tools(exposed_name);
CREATE INDEX IF NOT EXISTS tools_server_idx ON tools(server_id);
CREATE TABLE IF NOT EXISTS resources (
  server_id TEXT NOT NULL,
  uri TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  mime_type TEXT NOT NULL DEFAULT '',
  annotations BLOB,
  payload BLOB,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(server_id, uri),
  FOREIGN KEY(server_id) REFERENCES servers(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS resource_templates (
  server_id TEXT NOT NULL,
  uri_template TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  mime_type TEXT NOT NULL DEFAULT '',
  annotations BLOB,
  payload BLOB,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(server_id, uri_template),
  FOREIGN KEY(server_id) REFERENCES servers(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS prompts (
  server_id TEXT NOT NULL,
  name TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  arguments BLOB,
  payload BLOB,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(server_id, name),
  FOREIGN KEY(server_id) REFERENCES servers(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at INTEGER NOT NULL
);
INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, unixepoch());
`
	if err := s.exec(schema); err != nil {
		return err
	}
	if err := s.ensureColumn("tools", "search_text", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return s.exec("UPDATE tools SET search_text=lower(exposed_name || ' ' || title || ' ' || description) WHERE search_text=''")
}

func (s *Store) ensureColumn(table, column, declaration string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stmt, err := s.prepareLocked("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	found := false
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			break
		}
		if rc != C.SQLITE_ROW {
			C.sqlite3_finalize(stmt)
			return s.err(rc)
		}
		if textCol(stmt, 1) == column {
			found = true
			break
		}
	}
	C.sqlite3_finalize(stmt)
	if found {
		return nil
	}
	return s.execLocked("ALTER TABLE " + table + " ADD COLUMN " + column + " " + declaration)
}

func (s *Store) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.db == nil {
		return errors.New("sqlite store is closed")
	}
	stmt, err := s.prepareLocked("SELECT 1")
	if err != nil {
		return err
	}
	defer C.sqlite3_finalize(stmt)
	if rc := C.sqlite3_step(stmt); rc != C.SQLITE_ROW {
		return s.err(rc)
	}
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.db == nil {
		return nil
	}
	rc := C.sqlite3_close_v2(s.db)
	s.db = nil
	if rc != C.SQLITE_OK {
		return fmt.Errorf("sqlite close: code %d", int(rc))
	}
	return nil
}

func (s *Store) UpsertServer(ctx context.Context, server model.Server) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stmt, err := s.prepareLocked(`INSERT INTO servers(
 id,name,transport,status,status_message,endpoint,protocol_version,server_version,capabilities,
 tool_count,resource_count,resource_template_count,prompt_count,last_health_check,last_discovered_at,connected_since,config_hash
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
 name=excluded.name,transport=excluded.transport,status=excluded.status,status_message=excluded.status_message,
 endpoint=excluded.endpoint,protocol_version=excluded.protocol_version,server_version=excluded.server_version,
 capabilities=excluded.capabilities,tool_count=excluded.tool_count,resource_count=excluded.resource_count,
 resource_template_count=excluded.resource_template_count,prompt_count=excluded.prompt_count,
 last_health_check=excluded.last_health_check,last_discovered_at=excluded.last_discovered_at,
 connected_since=excluded.connected_since,config_hash=excluded.config_hash`)
	if err != nil {
		return err
	}
	defer C.sqlite3_finalize(stmt)
	values := []any{server.ID, server.Name, string(server.Transport), string(server.Status), server.StatusMessage, server.Endpoint,
		server.ProtocolVersion, server.ServerVersion, []byte(server.Capabilities), server.ToolCount, server.ResourceCount,
		server.ResourceTemplateCount, server.PromptCount, millis(server.LastHealthCheck), millis(server.LastDiscoveredAt), millis(server.ConnectedSince), server.ConfigHash}
	if err := bindAll(stmt, values...); err != nil {
		return err
	}
	if rc := C.sqlite3_step(stmt); rc != C.SQLITE_DONE {
		return s.err(rc)
	}
	return nil
}

func (s *Store) DeleteServer(ctx context.Context, id string) error {
	return s.execContext(ctx, "DELETE FROM servers WHERE id=?", id)
}

func (s *Store) GetServer(ctx context.Context, id string) (model.Server, error) {
	if err := ctx.Err(); err != nil {
		return model.Server{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stmt, err := s.prepareLocked(serverSelect + " WHERE id=?")
	if err != nil {
		return model.Server{}, err
	}
	defer C.sqlite3_finalize(stmt)
	if err := bindAll(stmt, id); err != nil {
		return model.Server{}, err
	}
	rc := C.sqlite3_step(stmt)
	if rc == C.SQLITE_DONE {
		return model.Server{}, storage.ErrNotFound
	}
	if rc != C.SQLITE_ROW {
		return model.Server{}, s.err(rc)
	}
	return scanServer(stmt), nil
}

const serverSelect = `SELECT id,name,transport,status,status_message,endpoint,protocol_version,server_version,capabilities,
 tool_count,resource_count,resource_template_count,prompt_count,last_health_check,last_discovered_at,connected_since,config_hash FROM servers`

func (s *Store) ListServers(ctx context.Context) ([]model.Server, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stmt, err := s.prepareLocked(serverSelect + " ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer C.sqlite3_finalize(stmt)
	out := []model.Server{}
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			return out, nil
		}
		if rc != C.SQLITE_ROW {
			return nil, s.err(rc)
		}
		out = append(out, scanServer(stmt))
	}
}

func scanServer(stmt *C.sqlite3_stmt) model.Server {
	return model.Server{
		ID: textCol(stmt, 0), Name: textCol(stmt, 1), Transport: model.TransportType(textCol(stmt, 2)), Status: model.HealthStatus(textCol(stmt, 3)),
		StatusMessage: textCol(stmt, 4), Endpoint: textCol(stmt, 5), ProtocolVersion: textCol(stmt, 6), ServerVersion: textCol(stmt, 7),
		Capabilities: json.RawMessage(blobCol(stmt, 8)), ToolCount: int(C.sqlite3_column_int(stmt, 9)), ResourceCount: int(C.sqlite3_column_int(stmt, 10)),
		ResourceTemplateCount: int(C.sqlite3_column_int(stmt, 11)), PromptCount: int(C.sqlite3_column_int(stmt, 12)),
		LastHealthCheck: fromMillis(int64(C.sqlite3_column_int64(stmt, 13))), LastDiscoveredAt: fromMillis(int64(C.sqlite3_column_int64(stmt, 14))),
		ConnectedSince: fromMillis(int64(C.sqlite3_column_int64(stmt, 15))), ConfigHash: textCol(stmt, 16),
	}
}

func (s *Store) ReplaceTools(ctx context.Context, serverID string, tools []model.Tool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transactionLocked(func() error {
		if err := s.execPreparedLocked("DELETE FROM tools WHERE server_id=?", serverID); err != nil {
			return err
		}
		stmt, err := s.prepareLocked(`INSERT INTO tools(server_id,name,exposed_name,title,description,search_text,input_schema,output_schema,annotations,schema_hash,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer C.sqlite3_finalize(stmt)
		for _, tool := range tools {
			C.sqlite3_reset(stmt)
			C.sqlite3_clear_bindings(stmt)
			if err := bindAll(stmt, tool.ServerID, tool.Name, tool.ExposedName, tool.Title, tool.Description, storage.ToolSearchText(tool), []byte(tool.InputSchema), []byte(tool.OutputSchema), []byte(tool.Annotations), tool.SchemaHash, millis(tool.UpdatedAt)); err != nil {
				return err
			}
			if rc := C.sqlite3_step(stmt); rc != C.SQLITE_DONE {
				return s.err(rc)
			}
		}
		return nil
	})
}

func (s *Store) ListToolSummaries(ctx context.Context, serverID, query string, limit, offset int) ([]model.ToolSummary, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	where, args := toolWhere(serverID, query)
	countStmt, err := s.prepareLocked("SELECT COUNT(*) FROM tools" + where)
	if err != nil {
		return nil, 0, err
	}
	if err := bindAll(countStmt, args...); err != nil {
		C.sqlite3_finalize(countStmt)
		return nil, 0, err
	}
	if rc := C.sqlite3_step(countStmt); rc != C.SQLITE_ROW {
		C.sqlite3_finalize(countStmt)
		return nil, 0, s.err(rc)
	}
	total := int(C.sqlite3_column_int(countStmt, 0))
	C.sqlite3_finalize(countStmt)
	stmt, err := s.prepareLocked(`SELECT server_id,name,exposed_name,title,description,schema_hash,updated_at FROM tools` + where + ` ORDER BY exposed_name COLLATE NOCASE LIMIT ? OFFSET ?`)
	if err != nil {
		return nil, 0, err
	}
	defer C.sqlite3_finalize(stmt)
	listArgs := append(append([]any{}, args...), limit, offset)
	if err := bindAll(stmt, listArgs...); err != nil {
		return nil, 0, err
	}
	out := []model.ToolSummary{}
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			return out, total, nil
		}
		if rc != C.SQLITE_ROW {
			return nil, 0, s.err(rc)
		}
		out = append(out, model.ToolSummary{ServerID: textCol(stmt, 0), Name: textCol(stmt, 1), ExposedName: textCol(stmt, 2), Title: textCol(stmt, 3), Description: textCol(stmt, 4), SchemaHash: textCol(stmt, 5), UpdatedAt: fromMillis(int64(C.sqlite3_column_int64(stmt, 6)))})
	}
}

func toolWhere(serverID, query string) (string, []any) {
	parts := []string{}
	args := []any{}
	if serverID != "" {
		parts = append(parts, "server_id=?")
		args = append(args, serverID)
	}
	if strings.TrimSpace(query) != "" {
		q := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
		parts = append(parts, "search_text LIKE ?")
		args = append(args, q)
	}
	if len(parts) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(parts, " AND "), args
}

func (s *Store) GetTool(ctx context.Context, serverID, name string) (model.Tool, error) {
	if err := ctx.Err(); err != nil {
		return model.Tool{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stmt, err := s.prepareLocked(`SELECT server_id,name,exposed_name,title,description,input_schema,output_schema,annotations,schema_hash,updated_at FROM tools WHERE server_id=? AND name=?`)
	if err != nil {
		return model.Tool{}, err
	}
	defer C.sqlite3_finalize(stmt)
	if err := bindAll(stmt, serverID, name); err != nil {
		return model.Tool{}, err
	}
	rc := C.sqlite3_step(stmt)
	if rc == C.SQLITE_DONE {
		return model.Tool{}, storage.ErrNotFound
	}
	if rc != C.SQLITE_ROW {
		return model.Tool{}, s.err(rc)
	}
	return scanTool(stmt), nil
}

func (s *Store) ListTools(ctx context.Context, serverID string) ([]model.Tool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	query := `SELECT server_id,name,exposed_name,title,description,input_schema,output_schema,annotations,schema_hash,updated_at FROM tools`
	args := []any{}
	if serverID != "" {
		query += " WHERE server_id=?"
		args = append(args, serverID)
	}
	query += " ORDER BY exposed_name COLLATE NOCASE"
	stmt, err := s.prepareLocked(query)
	if err != nil {
		return nil, err
	}
	defer C.sqlite3_finalize(stmt)
	if err := bindAll(stmt, args...); err != nil {
		return nil, err
	}
	out := []model.Tool{}
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			return out, nil
		}
		if rc != C.SQLITE_ROW {
			return nil, s.err(rc)
		}
		out = append(out, scanTool(stmt))
	}
}

func (s *Store) ListToolsPage(ctx context.Context, serverID string, limit, offset int) ([]model.Tool, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	where := ""
	args := []any{}
	if serverID != "" {
		where = " WHERE server_id=?"
		args = append(args, serverID)
	}
	total, err := s.countLocked("tools", where, args...)
	if err != nil {
		return nil, 0, err
	}
	stmt, err := s.prepareLocked(`SELECT server_id,name,exposed_name,title,description,input_schema,output_schema,annotations,schema_hash,updated_at FROM tools` + where + ` ORDER BY exposed_name COLLATE NOCASE LIMIT ? OFFSET ?`)
	if err != nil {
		return nil, 0, err
	}
	defer C.sqlite3_finalize(stmt)
	args = append(args, limit, offset)
	if err := bindAll(stmt, args...); err != nil {
		return nil, 0, err
	}
	out := []model.Tool{}
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			return out, total, nil
		}
		if rc != C.SQLITE_ROW {
			return nil, 0, s.err(rc)
		}
		out = append(out, scanTool(stmt))
	}
}

func scanTool(stmt *C.sqlite3_stmt) model.Tool {
	return model.Tool{ServerID: textCol(stmt, 0), Name: textCol(stmt, 1), ExposedName: textCol(stmt, 2), Title: textCol(stmt, 3), Description: textCol(stmt, 4), InputSchema: json.RawMessage(blobCol(stmt, 5)), OutputSchema: json.RawMessage(blobCol(stmt, 6)), Annotations: json.RawMessage(blobCol(stmt, 7)), SchemaHash: textCol(stmt, 8), UpdatedAt: fromMillis(int64(C.sqlite3_column_int64(stmt, 9)))}
}

func (s *Store) ReplaceResources(ctx context.Context, serverID string, items []model.Resource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transactionLocked(func() error {
		if err := s.execPreparedLocked("DELETE FROM resources WHERE server_id=?", serverID); err != nil {
			return err
		}
		stmt, err := s.prepareLocked(`INSERT INTO resources(server_id,uri,name,title,description,mime_type,annotations,payload,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer C.sqlite3_finalize(stmt)
		for _, v := range items {
			C.sqlite3_reset(stmt)
			C.sqlite3_clear_bindings(stmt)
			if err := bindAll(stmt, v.ServerID, v.URI, v.Name, v.Title, v.Description, v.MIMEType, []byte(v.Annotations), []byte(v.Payload), millis(v.UpdatedAt)); err != nil {
				return err
			}
			if rc := C.sqlite3_step(stmt); rc != C.SQLITE_DONE {
				return s.err(rc)
			}
		}
		return nil
	})
}
func (s *Store) ListResources(ctx context.Context, serverID string, limit, offset int) ([]model.Resource, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	where := ""
	args := []any{}
	if serverID != "" {
		where = " WHERE server_id=?"
		args = append(args, serverID)
	}
	total, err := s.countLocked("resources", where, args...)
	if err != nil {
		return nil, 0, err
	}
	stmt, err := s.prepareLocked(`SELECT server_id,uri,name,title,description,mime_type,annotations,payload,updated_at FROM resources` + where + ` ORDER BY uri COLLATE NOCASE LIMIT ? OFFSET ?`)
	if err != nil {
		return nil, 0, err
	}
	defer C.sqlite3_finalize(stmt)
	args = append(args, limit, offset)
	if err := bindAll(stmt, args...); err != nil {
		return nil, 0, err
	}
	out := []model.Resource{}
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			return out, total, nil
		}
		if rc != C.SQLITE_ROW {
			return nil, 0, s.err(rc)
		}
		out = append(out, model.Resource{ServerID: textCol(stmt, 0), URI: textCol(stmt, 1), Name: textCol(stmt, 2), Title: textCol(stmt, 3), Description: textCol(stmt, 4), MIMEType: textCol(stmt, 5), Annotations: json.RawMessage(blobCol(stmt, 6)), Payload: json.RawMessage(blobCol(stmt, 7)), UpdatedAt: fromMillis(int64(C.sqlite3_column_int64(stmt, 8)))})
	}
}
func (s *Store) ReplaceResourceTemplates(ctx context.Context, serverID string, items []model.ResourceTemplate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transactionLocked(func() error {
		if err := s.execPreparedLocked("DELETE FROM resource_templates WHERE server_id=?", serverID); err != nil {
			return err
		}
		stmt, err := s.prepareLocked(`INSERT INTO resource_templates(server_id,uri_template,name,title,description,mime_type,annotations,payload,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer C.sqlite3_finalize(stmt)
		for _, v := range items {
			C.sqlite3_reset(stmt)
			C.sqlite3_clear_bindings(stmt)
			if err := bindAll(stmt, v.ServerID, v.URITemplate, v.Name, v.Title, v.Description, v.MIMEType, []byte(v.Annotations), []byte(v.Payload), millis(v.UpdatedAt)); err != nil {
				return err
			}
			if rc := C.sqlite3_step(stmt); rc != C.SQLITE_DONE {
				return s.err(rc)
			}
		}
		return nil
	})
}
func (s *Store) ListResourceTemplates(ctx context.Context, serverID string, limit, offset int) ([]model.ResourceTemplate, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	where := ""
	args := []any{}
	if serverID != "" {
		where = " WHERE server_id=?"
		args = append(args, serverID)
	}
	total, err := s.countLocked("resource_templates", where, args...)
	if err != nil {
		return nil, 0, err
	}
	stmt, err := s.prepareLocked(`SELECT server_id,uri_template,name,title,description,mime_type,annotations,payload,updated_at FROM resource_templates` + where + ` ORDER BY uri_template COLLATE NOCASE LIMIT ? OFFSET ?`)
	if err != nil {
		return nil, 0, err
	}
	defer C.sqlite3_finalize(stmt)
	args = append(args, limit, offset)
	if err := bindAll(stmt, args...); err != nil {
		return nil, 0, err
	}
	out := []model.ResourceTemplate{}
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			return out, total, nil
		}
		if rc != C.SQLITE_ROW {
			return nil, 0, s.err(rc)
		}
		out = append(out, model.ResourceTemplate{ServerID: textCol(stmt, 0), URITemplate: textCol(stmt, 1), Name: textCol(stmt, 2), Title: textCol(stmt, 3), Description: textCol(stmt, 4), MIMEType: textCol(stmt, 5), Annotations: json.RawMessage(blobCol(stmt, 6)), Payload: json.RawMessage(blobCol(stmt, 7)), UpdatedAt: fromMillis(int64(C.sqlite3_column_int64(stmt, 8)))})
	}
}
func (s *Store) ReplacePrompts(ctx context.Context, serverID string, items []model.Prompt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transactionLocked(func() error {
		if err := s.execPreparedLocked("DELETE FROM prompts WHERE server_id=?", serverID); err != nil {
			return err
		}
		stmt, err := s.prepareLocked(`INSERT INTO prompts(server_id,name,title,description,arguments,payload,updated_at) VALUES(?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer C.sqlite3_finalize(stmt)
		for _, v := range items {
			C.sqlite3_reset(stmt)
			C.sqlite3_clear_bindings(stmt)
			if err := bindAll(stmt, v.ServerID, v.Name, v.Title, v.Description, []byte(v.Arguments), []byte(v.Payload), millis(v.UpdatedAt)); err != nil {
				return err
			}
			if rc := C.sqlite3_step(stmt); rc != C.SQLITE_DONE {
				return s.err(rc)
			}
		}
		return nil
	})
}
func (s *Store) ListPrompts(ctx context.Context, serverID string, limit, offset int) ([]model.Prompt, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	where := ""
	args := []any{}
	if serverID != "" {
		where = " WHERE server_id=?"
		args = append(args, serverID)
	}
	total, err := s.countLocked("prompts", where, args...)
	if err != nil {
		return nil, 0, err
	}
	stmt, err := s.prepareLocked(`SELECT server_id,name,title,description,arguments,payload,updated_at FROM prompts` + where + ` ORDER BY name COLLATE NOCASE LIMIT ? OFFSET ?`)
	if err != nil {
		return nil, 0, err
	}
	defer C.sqlite3_finalize(stmt)
	args = append(args, limit, offset)
	if err := bindAll(stmt, args...); err != nil {
		return nil, 0, err
	}
	out := []model.Prompt{}
	for {
		rc := C.sqlite3_step(stmt)
		if rc == C.SQLITE_DONE {
			return out, total, nil
		}
		if rc != C.SQLITE_ROW {
			return nil, 0, s.err(rc)
		}
		out = append(out, model.Prompt{ServerID: textCol(stmt, 0), Name: textCol(stmt, 1), Title: textCol(stmt, 2), Description: textCol(stmt, 3), Arguments: json.RawMessage(blobCol(stmt, 4)), Payload: json.RawMessage(blobCol(stmt, 5)), UpdatedAt: fromMillis(int64(C.sqlite3_column_int64(stmt, 6)))})
	}
}

func (s *Store) countLocked(table, where string, args ...any) (int, error) {
	stmt, err := s.prepareLocked("SELECT COUNT(*) FROM " + table + where)
	if err != nil {
		return 0, err
	}
	defer C.sqlite3_finalize(stmt)
	if err := bindAll(stmt, args...); err != nil {
		return 0, err
	}
	if rc := C.sqlite3_step(stmt); rc != C.SQLITE_ROW {
		return 0, s.err(rc)
	}
	return int(C.sqlite3_column_int(stmt, 0)), nil
}
func (s *Store) execContext(ctx context.Context, query string, args ...any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.execPreparedLocked(query, args...)
}
func (s *Store) exec(query string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.execLocked(query)
}
func (s *Store) execLocked(query string) error {
	cquery := C.CString(query)
	defer C.free(unsafe.Pointer(cquery))
	var errmsg *C.char
	rc := C.sqlite3_exec(s.db, cquery, nil, nil, &errmsg)
	if rc != C.SQLITE_OK {
		message := "sqlite error"
		if errmsg != nil {
			message = C.GoString(errmsg)
			C.sqlite3_free(unsafe.Pointer(errmsg))
		}
		return fmt.Errorf("sqlite: %s", message)
	}
	return nil
}
func (s *Store) execPreparedLocked(query string, args ...any) error {
	stmt, err := s.prepareLocked(query)
	if err != nil {
		return err
	}
	defer C.sqlite3_finalize(stmt)
	if err := bindAll(stmt, args...); err != nil {
		return err
	}
	if rc := C.sqlite3_step(stmt); rc != C.SQLITE_DONE {
		return s.err(rc)
	}
	return nil
}
func (s *Store) prepareLocked(query string) (*C.sqlite3_stmt, error) {
	if s.closed || s.db == nil {
		return nil, errors.New("sqlite store is closed")
	}
	cquery := C.CString(query)
	defer C.free(unsafe.Pointer(cquery))
	var stmt *C.sqlite3_stmt
	rc := C.sqlite3_prepare_v2(s.db, cquery, -1, &stmt, nil)
	if rc != C.SQLITE_OK {
		return nil, s.err(rc)
	}
	return stmt, nil
}
func (s *Store) transactionLocked(fn func() error) error {
	if err := s.execLocked("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := fn(); err != nil {
		_ = s.execLocked("ROLLBACK")
		return err
	}
	if err := s.execLocked("COMMIT"); err != nil {
		_ = s.execLocked("ROLLBACK")
		return err
	}
	return nil
}
func (s *Store) err(rc C.int) error {
	if s.db == nil {
		return fmt.Errorf("sqlite error %d", int(rc))
	}
	return fmt.Errorf("sqlite: %s (code %d)", C.GoString(C.sqlite3_errmsg(s.db)), int(rc))
}

func bindAll(stmt *C.sqlite3_stmt, values ...any) error {
	for i, value := range values {
		idx := C.int(i + 1)
		var rc C.int
		switch v := value.(type) {
		case nil:
			rc = C.sqlite3_bind_null(stmt, idx)
		case string:
			cv := C.CString(v)
			rc = C.bind_text_transient(stmt, idx, cv, C.int(len(v)))
			C.free(unsafe.Pointer(cv))
		case []byte:
			if len(v) == 0 {
				rc = C.sqlite3_bind_null(stmt, idx)
			} else {
				ptr := C.CBytes(v)
				rc = C.bind_blob_transient(stmt, idx, ptr, C.int(len(v)))
				C.free(ptr)
			}
		case int:
			rc = C.sqlite3_bind_int64(stmt, idx, C.sqlite3_int64(v))
		case int64:
			rc = C.sqlite3_bind_int64(stmt, idx, C.sqlite3_int64(v))
		case bool:
			if v {
				rc = C.sqlite3_bind_int(stmt, idx, 1)
			} else {
				rc = C.sqlite3_bind_int(stmt, idx, 0)
			}
		default:
			return fmt.Errorf("sqlite: unsupported bind type %T", value)
		}
		if rc != C.SQLITE_OK {
			return fmt.Errorf("sqlite bind parameter %d failed: code %d", i+1, int(rc))
		}
	}
	return nil
}
func textCol(stmt *C.sqlite3_stmt, index int) string {
	ptr := C.sqlite3_column_text(stmt, C.int(index))
	if ptr == nil {
		return ""
	}
	return C.GoString((*C.char)(unsafe.Pointer(ptr)))
}
func blobCol(stmt *C.sqlite3_stmt, index int) []byte {
	ptr := C.sqlite3_column_blob(stmt, C.int(index))
	size := C.sqlite3_column_bytes(stmt, C.int(index))
	if ptr == nil || size <= 0 {
		return nil
	}
	return C.GoBytes(ptr, size)
}
func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
func fromMillis(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}
