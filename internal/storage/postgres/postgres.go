//go:build postgres

package postgres

/*
#cgo pkg-config: libpq
#include <stdlib.h>
#include <libpq-fe.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/sid-pani/lessen-mcp-gateway/internal/model"
	"github.com/sid-pani/lessen-mcp-gateway/internal/storage"
)

// Store is the optional external PostgreSQL persistence adapter. It intentionally
// serializes access to one libpq connection: registry operations are small and
// infrequent, while a single connection keeps the gateway's memory and socket
// footprint predictable. Deployments needing more throughput can run multiple
// gateway instances against the same database.
type Store struct {
	mu     sync.Mutex
	conn   *C.PGconn
	closed bool
}

func Open(dsn string) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("postgres DSN is empty")
	}
	cdsn := C.CString(dsn)
	defer C.free(unsafe.Pointer(cdsn))
	conn := C.PQconnectdb(cdsn)
	if conn == nil {
		return nil, errors.New("postgres: PQconnectdb returned nil")
	}
	if C.PQstatus(conn) != C.CONNECTION_OK {
		message := strings.TrimSpace(C.GoString(C.PQerrorMessage(conn)))
		C.PQfinish(conn)
		return nil, fmt.Errorf("postgres: %s", message)
	}
	s := &Store{conn: conn}
	if err := s.migrate(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commandLocked(`
CREATE TABLE IF NOT EXISTS mcp_gateway_servers (
  id TEXT PRIMARY KEY,
  payload JSONB NOT NULL
);
CREATE TABLE IF NOT EXISTS mcp_gateway_tools (
  server_id TEXT NOT NULL REFERENCES mcp_gateway_servers(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  exposed_name TEXT NOT NULL,
  searchable TEXT NOT NULL,
  payload JSONB NOT NULL,
  PRIMARY KEY(server_id, name)
);
CREATE INDEX IF NOT EXISTS mcp_gateway_tools_exposed_idx ON mcp_gateway_tools(exposed_name);
CREATE INDEX IF NOT EXISTS mcp_gateway_tools_search_idx ON mcp_gateway_tools(server_id, exposed_name);
CREATE TABLE IF NOT EXISTS mcp_gateway_resources (
  server_id TEXT NOT NULL REFERENCES mcp_gateway_servers(id) ON DELETE CASCADE,
  uri TEXT NOT NULL,
  payload JSONB NOT NULL,
  PRIMARY KEY(server_id, uri)
);
CREATE TABLE IF NOT EXISTS mcp_gateway_resource_templates (
  server_id TEXT NOT NULL REFERENCES mcp_gateway_servers(id) ON DELETE CASCADE,
  uri_template TEXT NOT NULL,
  payload JSONB NOT NULL,
  PRIMARY KEY(server_id, uri_template)
);
CREATE TABLE IF NOT EXISTS mcp_gateway_prompts (
  server_id TEXT NOT NULL REFERENCES mcp_gateway_servers(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  payload JSONB NOT NULL,
  PRIMARY KEY(server_id, name)
);`)
}

func (s *Store) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.queryLocked("SELECT 1")
	if result != nil {
		C.PQclear(result)
	}
	return err
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.conn != nil {
		C.PQfinish(s.conn)
		s.conn = nil
	}
	return nil
}

func (s *Store) UpsertServer(ctx context.Context, server model.Server) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(server)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commandParamsLocked(`INSERT INTO mcp_gateway_servers(id,payload) VALUES($1,$2::jsonb)
ON CONFLICT(id) DO UPDATE SET payload=EXCLUDED.payload`, server.ID, string(payload))
}

func (s *Store) DeleteServer(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commandParamsLocked("DELETE FROM mcp_gateway_servers WHERE id=$1", id)
}

func (s *Store) GetServer(ctx context.Context, id string) (model.Server, error) {
	if err := ctx.Err(); err != nil {
		return model.Server{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.queryParamsLocked("SELECT payload::text FROM mcp_gateway_servers WHERE id=$1", id)
	if err != nil {
		return model.Server{}, err
	}
	defer C.PQclear(result)
	if rows(result) == 0 {
		return model.Server{}, storage.ErrNotFound
	}
	return decodeRow[model.Server](result, 0, 0)
}

func (s *Store) ListServers(ctx context.Context) ([]model.Server, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.queryLocked("SELECT payload::text FROM mcp_gateway_servers ORDER BY lower(payload->>'name')")
	if err != nil {
		return nil, err
	}
	defer C.PQclear(result)
	return decodeRows[model.Server](result, 0)
}

func (s *Store) ReplaceTools(ctx context.Context, serverID string, items []model.Tool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transactionLocked(func() error {
		if err := s.commandParamsLocked("DELETE FROM mcp_gateway_tools WHERE server_id=$1", serverID); err != nil {
			return err
		}
		for _, item := range items {
			payload, err := json.Marshal(item)
			if err != nil {
				return err
			}
			searchable := storage.ToolSearchText(item)
			if err := s.commandParamsLocked(`INSERT INTO mcp_gateway_tools(server_id,name,exposed_name,searchable,payload)
VALUES($1,$2,$3,$4,$5::jsonb)`, serverID, item.Name, item.ExposedName, searchable, string(payload)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) ListToolSummaries(ctx context.Context, serverID, query string, limit, offset int) ([]model.ToolSummary, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	limit, offset = pageBounds(limit, offset)
	where, args := postgresToolWhere(serverID, query)
	s.mu.Lock()
	defer s.mu.Unlock()
	total, err := s.countLocked("mcp_gateway_tools", where, args...)
	if err != nil {
		return nil, 0, err
	}
	args = append(args, strconv.Itoa(limit), strconv.Itoa(offset))
	result, err := s.queryParamsLocked("SELECT payload::text FROM mcp_gateway_tools"+where+fmt.Sprintf(" ORDER BY exposed_name LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer C.PQclear(result)
	tools, err := decodeRows[model.Tool](result, 0)
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.ToolSummary, 0, len(tools))
	for _, item := range tools {
		out = append(out, model.ToolSummary{ServerID: item.ServerID, Name: item.Name, ExposedName: item.ExposedName, Title: item.Title, Description: item.Description, SchemaHash: item.SchemaHash, UpdatedAt: item.UpdatedAt})
	}
	return out, total, nil
}

func (s *Store) GetTool(ctx context.Context, serverID, name string) (model.Tool, error) {
	if err := ctx.Err(); err != nil {
		return model.Tool{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.queryParamsLocked("SELECT payload::text FROM mcp_gateway_tools WHERE server_id=$1 AND name=$2", serverID, name)
	if err != nil {
		return model.Tool{}, err
	}
	defer C.PQclear(result)
	if rows(result) == 0 {
		return model.Tool{}, storage.ErrNotFound
	}
	return decodeRow[model.Tool](result, 0, 0)
}

func (s *Store) ListTools(ctx context.Context, serverID string) ([]model.Tool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query := "SELECT payload::text FROM mcp_gateway_tools"
	args := []string{}
	if serverID != "" {
		query += " WHERE server_id=$1"
		args = append(args, serverID)
	}
	query += " ORDER BY exposed_name"
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.queryParamsLocked(query, args...)
	if err != nil {
		return nil, err
	}
	defer C.PQclear(result)
	return decodeRows[model.Tool](result, 0)
}

func (s *Store) ListToolsPage(ctx context.Context, serverID string, limit, offset int) ([]model.Tool, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	limit, offset = pageBounds(limit, offset)
	where := ""
	args := []string{}
	if serverID != "" {
		where = " WHERE server_id=$1"
		args = append(args, serverID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	total, err := s.countLocked("mcp_gateway_tools", where, args...)
	if err != nil {
		return nil, 0, err
	}
	args = append(args, strconv.Itoa(limit), strconv.Itoa(offset))
	result, err := s.queryParamsLocked("SELECT payload::text FROM mcp_gateway_tools"+where+fmt.Sprintf(" ORDER BY exposed_name LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer C.PQclear(result)
	items, err := decodeRows[model.Tool](result, 0)
	return items, total, err
}

func (s *Store) ReplaceResources(ctx context.Context, serverID string, items []model.Resource) error {
	return s.replaceResources(ctx, "mcp_gateway_resources", "uri", serverID, len(items), func(i int) (string, any) { return items[i].URI, items[i] })
}
func (s *Store) ListResources(ctx context.Context, serverID string, limit, offset int) ([]model.Resource, int, error) {
	return listPayloadPage[model.Resource](ctx, s, "mcp_gateway_resources", "uri", serverID, limit, offset)
}
func (s *Store) ReplaceResourceTemplates(ctx context.Context, serverID string, items []model.ResourceTemplate) error {
	return s.replaceResources(ctx, "mcp_gateway_resource_templates", "uri_template", serverID, len(items), func(i int) (string, any) { return items[i].URITemplate, items[i] })
}
func (s *Store) ListResourceTemplates(ctx context.Context, serverID string, limit, offset int) ([]model.ResourceTemplate, int, error) {
	return listPayloadPage[model.ResourceTemplate](ctx, s, "mcp_gateway_resource_templates", "uri_template", serverID, limit, offset)
}
func (s *Store) ReplacePrompts(ctx context.Context, serverID string, items []model.Prompt) error {
	return s.replaceResources(ctx, "mcp_gateway_prompts", "name", serverID, len(items), func(i int) (string, any) { return items[i].Name, items[i] })
}
func (s *Store) ListPrompts(ctx context.Context, serverID string, limit, offset int) ([]model.Prompt, int, error) {
	return listPayloadPage[model.Prompt](ctx, s, "mcp_gateway_prompts", "name", serverID, limit, offset)
}

func (s *Store) replaceResources(ctx context.Context, table, keyColumn, serverID string, count int, at func(int) (string, any)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validPayloadTable(table, keyColumn) {
		return errors.New("invalid postgres registry table")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transactionLocked(func() error {
		if err := s.commandParamsLocked("DELETE FROM "+table+" WHERE server_id=$1", serverID); err != nil {
			return err
		}
		for i := 0; i < count; i++ {
			key, value := at(i)
			payload, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if err := s.commandParamsLocked("INSERT INTO "+table+"(server_id,"+keyColumn+",payload) VALUES($1,$2,$3::jsonb)", serverID, key, string(payload)); err != nil {
				return err
			}
		}
		return nil
	})
}

func listPayloadPage[T any](ctx context.Context, s *Store, table, orderColumn, serverID string, limit, offset int) ([]T, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if !validPayloadTable(table, orderColumn) {
		return nil, 0, errors.New("invalid postgres registry table")
	}
	limit, offset = pageBounds(limit, offset)
	where := ""
	args := []string{}
	if serverID != "" {
		where = " WHERE server_id=$1"
		args = append(args, serverID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	total, err := s.countLocked(table, where, args...)
	if err != nil {
		return nil, 0, err
	}
	args = append(args, strconv.Itoa(limit), strconv.Itoa(offset))
	result, err := s.queryParamsLocked("SELECT payload::text FROM "+table+where+fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", orderColumn, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer C.PQclear(result)
	items, err := decodeRows[T](result, 0)
	return items, total, err
}

func validPayloadTable(table, column string) bool {
	switch table + "/" + column {
	case "mcp_gateway_resources/uri", "mcp_gateway_resource_templates/uri_template", "mcp_gateway_prompts/name":
		return true
	default:
		return false
	}
}

func postgresToolWhere(serverID, query string) (string, []string) {
	parts := []string{}
	args := []string{}
	if serverID != "" {
		args = append(args, serverID)
		parts = append(parts, fmt.Sprintf("server_id=$%d", len(args)))
	}
	if strings.TrimSpace(query) != "" {
		args = append(args, "%"+strings.ToLower(strings.TrimSpace(query))+"%")
		parts = append(parts, fmt.Sprintf("searchable LIKE $%d", len(args)))
	}
	if len(parts) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(parts, " AND "), args
}

func pageBounds(limit, offset int) (int, int) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func (s *Store) countLocked(table, where string, args ...string) (int, error) {
	result, err := s.queryParamsLocked("SELECT COUNT(*) FROM "+table+where, args...)
	if err != nil {
		return 0, err
	}
	defer C.PQclear(result)
	value, err := strconv.Atoi(text(result, 0, 0))
	if err != nil {
		return 0, fmt.Errorf("decode postgres count: %w", err)
	}
	return value, nil
}

func (s *Store) transactionLocked(fn func() error) error {
	if err := s.commandLocked("BEGIN"); err != nil {
		return err
	}
	if err := fn(); err != nil {
		_ = s.commandLocked("ROLLBACK")
		return err
	}
	if err := s.commandLocked("COMMIT"); err != nil {
		_ = s.commandLocked("ROLLBACK")
		return err
	}
	return nil
}

func (s *Store) commandLocked(query string) error {
	result, err := s.execLocked(query, nil, C.PGRES_COMMAND_OK)
	if result != nil {
		C.PQclear(result)
	}
	return err
}
func (s *Store) commandParamsLocked(query string, args ...string) error {
	result, err := s.execLocked(query, args, C.PGRES_COMMAND_OK)
	if result != nil {
		C.PQclear(result)
	}
	return err
}
func (s *Store) queryLocked(query string) (*C.PGresult, error) {
	return s.execLocked(query, nil, C.PGRES_TUPLES_OK)
}
func (s *Store) queryParamsLocked(query string, args ...string) (*C.PGresult, error) {
	return s.execLocked(query, args, C.PGRES_TUPLES_OK)
}

func (s *Store) execLocked(query string, args []string, expected C.ExecStatusType) (*C.PGresult, error) {
	if s.closed || s.conn == nil {
		return nil, errors.New("postgres store is closed")
	}
	cquery := C.CString(query)
	defer C.free(unsafe.Pointer(cquery))
	var result *C.PGresult
	if len(args) == 0 {
		result = C.PQexec(s.conn, cquery)
	} else {
		valuesMem := C.malloc(C.size_t(len(args)) * C.size_t(unsafe.Sizeof(uintptr(0))))
		if valuesMem == nil {
			return nil, errors.New("postgres: allocate parameter array")
		}
		defer C.free(valuesMem)
		values := unsafe.Slice((**C.char)(valuesMem), len(args))
		for i, arg := range args {
			values[i] = C.CString(arg)
			defer C.free(unsafe.Pointer(values[i]))
		}
		result = C.PQexecParams(s.conn, cquery, C.int(len(args)), nil, (**C.char)(valuesMem), nil, nil, 0)
	}
	if result == nil {
		return nil, fmt.Errorf("postgres: %s", strings.TrimSpace(C.GoString(C.PQerrorMessage(s.conn))))
	}
	if status := C.PQresultStatus(result); status != expected {
		message := strings.TrimSpace(C.GoString(C.PQresultErrorMessage(result)))
		C.PQclear(result)
		return nil, fmt.Errorf("postgres: %s", message)
	}
	return result, nil
}

func rows(result *C.PGresult) int { return int(C.PQntuples(result)) }
func text(result *C.PGresult, row, column int) string {
	if C.PQgetisnull(result, C.int(row), C.int(column)) != 0 {
		return ""
	}
	return C.GoString(C.PQgetvalue(result, C.int(row), C.int(column)))
}
func decodeRow[T any](result *C.PGresult, row, column int) (T, error) {
	var value T
	if err := json.Unmarshal([]byte(text(result, row, column)), &value); err != nil {
		return value, fmt.Errorf("decode postgres registry payload: %w", err)
	}
	return value, nil
}
func decodeRows[T any](result *C.PGresult, column int) ([]T, error) {
	out := make([]T, 0, rows(result))
	for row := 0; row < rows(result); row++ {
		value, err := decodeRow[T](result, row, column)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

var _ storage.Store = (*Store)(nil)
