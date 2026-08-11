package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sid-pani/lessen-mcp-gateway/internal/mcp"
)

type HTTPOptions struct {
	URL                       string
	Headers                   map[string]string
	Timeout                   time.Duration
	IdleTimeout               time.Duration
	MaxIdleConnections        int
	MaxIdleConnectionsPerHost int
	MaxConnectionsPerHost     int
	MaxResponseBytes          int64
	ProtocolVersion           string
	Logger                    *slog.Logger
}

type StreamableHTTPClient struct {
	opts      HTTPOptions
	client    *http.Client
	transport *http.Transport
	nextID    atomic.Uint64
	mu        sync.RWMutex
	sessionID string
	closed    bool
}

func NewStreamableHTTP(opts HTTPOptions) (*StreamableHTTPClient, error) {
	if strings.TrimSpace(opts.URL) == "" {
		return nil, errors.New("streamable HTTP URL is required")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 5 * time.Minute
	}
	if opts.MaxIdleConnections <= 0 {
		opts.MaxIdleConnections = 16
	}
	if opts.MaxIdleConnectionsPerHost <= 0 {
		opts.MaxIdleConnectionsPerHost = 2
	}
	if opts.MaxConnectionsPerHost <= 0 {
		opts.MaxConnectionsPerHost = 8
	}
	if opts.MaxResponseBytes <= 0 {
		opts.MaxResponseBytes = 32 << 20
	}
	if opts.ProtocolVersion == "" {
		opts.ProtocolVersion = mcp.ProtocolLatest
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          opts.MaxIdleConnections,
		MaxIdleConnsPerHost:   opts.MaxIdleConnectionsPerHost,
		MaxConnsPerHost:       opts.MaxConnectionsPerHost,
		IdleConnTimeout:       opts.IdleTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: opts.Timeout,
	}
	return &StreamableHTTPClient{opts: opts, transport: tr, client: &http.Client{Transport: tr}}, nil
}

func (c *StreamableHTTPClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := mcp.NumberID(c.nextID.Add(1))
	payload, err := mcp.NewRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	response, err := c.post(ctx, method, requestName(method, params), payload, true)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	c.captureSession(response)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, c.httpError(response)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	switch mediaType {
	case "text/event-stream":
		return c.readSSEForID(ctx, response.Body, id)
	case "application/json", "application/json-rpc", "":
		data, err := readLimited(response.Body, c.opts.MaxResponseBytes)
		if err != nil {
			return nil, err
		}
		return decodeRPCResult(data, id)
	default:
		data, _ := readLimited(response.Body, 4096)
		return nil, fmt.Errorf("unexpected MCP content type %q: %s", mediaType, strings.TrimSpace(string(data)))
	}
}

func (c *StreamableHTTPClient) Notify(ctx context.Context, method string, params any) error {
	payload, err := mcp.NewNotification(method, params)
	if err != nil {
		return err
	}
	response, err := c.post(ctx, method, requestName(method, params), payload, false)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	c.captureSession(response)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return c.httpError(response)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return nil
}

func (c *StreamableHTTPClient) Health(ctx context.Context) error {
	_, err := c.Call(ctx, "ping", map[string]any{})
	return err
}

func (c *StreamableHTTPClient) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.transport.CloseIdleConnections()
	return nil
}

func (c *StreamableHTTPClient) post(ctx context.Context, method, name string, payload []byte, expectResponse bool) (*http.Response, error) {
	c.mu.RLock()
	closed := c.closed
	session := c.sessionID
	protocol := c.opts.ProtocolVersion
	c.mu.RUnlock()
	if closed {
		return nil, errors.New("streamable HTTP client is closed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if expectResponse {
		req.Header.Set("Accept", "application/json, text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	req.Header.Set("MCP-Protocol-Version", protocol)
	req.Header.Set("Mcp-Method", method)
	if name != "" {
		req.Header.Set("Mcp-Name", name)
	}
	if session != "" && protocol != mcp.ProtocolLatest {
		req.Header.Set("Mcp-Session-Id", session)
	}
	for key, value := range c.opts.Headers {
		req.Header.Set(key, value)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("MCP HTTP request: %w", err)
	}
	return response, nil
}

func (c *StreamableHTTPClient) captureSession(response *http.Response) {
	c.mu.RLock()
	protocol := c.opts.ProtocolVersion
	c.mu.RUnlock()
	if protocol == mcp.ProtocolLatest {
		return
	}
	if value := response.Header.Get("Mcp-Session-Id"); value != "" {
		c.mu.Lock()
		c.sessionID = value
		c.mu.Unlock()
	}
}

func (c *StreamableHTTPClient) readSSEForID(ctx context.Context, body io.Reader, id mcp.ID) (json.RawMessage, error) {
	reader := bufio.NewReader(io.LimitReader(body, c.opts.MaxResponseBytes+1))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		event, err := ReadSSE(reader, c.opts.MaxResponseBytes)
		if err != nil {
			return nil, err
		}
		if len(event.Data) == 0 {
			continue
		}
		request, response, err := mcp.ParseEnvelope(event.Data)
		if err != nil {
			c.opts.Logger.Debug("ignoring non-RPC SSE event", "event", event.Event, "error", err)
			continue
		}
		if request != nil {
			continue
		}
		if response == nil || response.ID.String() != id.String() {
			continue
		}
		if response.Error != nil {
			return nil, response.Error
		}
		return response.Result, nil
	}
}

func (c *StreamableHTTPClient) httpError(response *http.Response) error {
	data, _ := readLimited(response.Body, 16*1024)
	message := strings.TrimSpace(string(data))
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	return fmt.Errorf("MCP HTTP %d: %s", response.StatusCode, message)
}

func readLimited(reader io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		max = 32 << 20
	}
	data, err := io.ReadAll(io.LimitReader(reader, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("response exceeds %d bytes", max)
	}
	return data, nil
}

func decodeRPCResult(data []byte, id mcp.ID) (json.RawMessage, error) {
	_, response, err := mcp.ParseEnvelope(data)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("MCP response is not a JSON-RPC response")
	}
	if response.ID.String() != id.String() {
		return nil, fmt.Errorf("MCP response id %s does not match request id %s", response.ID.String(), id.String())
	}
	if response.Error != nil {
		return nil, response.Error
	}
	return response.Result, nil
}

func (c *StreamableHTTPClient) SetProtocolVersion(version string) {
	if version == "" {
		return
	}
	c.mu.Lock()
	c.opts.ProtocolVersion = version
	if version == mcp.ProtocolLatest {
		c.sessionID = ""
	}
	c.mu.Unlock()
}

func requestName(method string, params any) string {
	if params == nil {
		return ""
	}
	data, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	return mcp.RequestName(method, data)
}
