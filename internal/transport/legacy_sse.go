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
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sid-pani/lessen-mcp-gateway/internal/mcp"
)

type LegacySSEClient struct {
	opts      HTTPOptions
	client    *http.Client
	transport *http.Transport
	nextID    atomic.Uint64

	mu         sync.Mutex
	closed     bool
	running    bool
	generation uint64
	endpoint   string
	ready      chan error
	cancel     context.CancelFunc
	pending    map[string]chan callResult
}

func NewLegacySSE(opts HTTPOptions) (*LegacySSEClient, error) {
	if strings.TrimSpace(opts.URL) == "" {
		return nil, errors.New("legacy SSE URL is required")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 5 * time.Minute
	}
	if opts.MaxResponseBytes <= 0 {
		opts.MaxResponseBytes = 32 << 20
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 8, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4, IdleConnTimeout: opts.IdleTimeout, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second}
	return &LegacySSEClient{opts: opts, client: &http.Client{Transport: tr}, transport: tr, pending: map[string]chan callResult{}}, nil
}

func (c *LegacySSEClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := c.ensureStream(ctx); err != nil {
		return nil, err
	}
	id := mcp.NumberID(c.nextID.Add(1))
	payload, err := mcp.NewRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	ch := make(chan callResult, 1)
	key := id.String()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("legacy SSE client is closed")
	}
	c.pending[key] = ch
	endpoint := c.endpoint
	c.mu.Unlock()
	if err := c.post(ctx, endpoint, payload, id); err != nil {
		c.removePending(key)
		return nil, err
	}
	select {
	case <-ctx.Done():
		c.removePending(key)
		return nil, ctx.Err()
	case result := <-ch:
		return result.result, result.err
	}
}
func (c *LegacySSEClient) Notify(ctx context.Context, method string, params any) error {
	if err := c.ensureStream(ctx); err != nil {
		return err
	}
	payload, err := mcp.NewNotification(method, params)
	if err != nil {
		return err
	}
	c.mu.Lock()
	endpoint := c.endpoint
	c.mu.Unlock()
	return c.post(ctx, endpoint, payload, nil)
}
func (c *LegacySSEClient) Health(ctx context.Context) error {
	_, err := c.Call(ctx, "ping", map[string]any{})
	return err
}
func (c *LegacySSEClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	pending := c.pending
	c.pending = map[string]chan callResult{}
	c.mu.Unlock()
	c.transport.CloseIdleConnections()
	for _, ch := range pending {
		ch <- callResult{err: errors.New("legacy SSE client closed")}
	}
	return nil
}

func (c *LegacySSEClient) ensureStream(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("legacy SSE client is closed")
	}
	if !c.running {
		streamCtx, cancel := context.WithCancel(context.Background())
		c.running = true
		c.generation++
		generation := c.generation
		c.ready = make(chan error, 1)
		c.cancel = cancel
		go c.streamLoop(streamCtx, generation)
	}
	ready := c.ready
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-ready:
		if err == nil {
			c.mu.Lock()
			if c.running {
				c.ready = ready
				select {
				case ready <- nil:
				default:
				}
			}
			c.mu.Unlock()
		}
		return err
	}
}

func (c *LegacySSEClient) streamLoop(ctx context.Context, generation uint64) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.opts.URL, nil)
	if err != nil {
		c.failStream(generation, err)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range c.opts.Headers {
		req.Header.Set(k, v)
	}
	response, err := c.client.Do(req)
	if err != nil {
		c.failStream(generation, fmt.Errorf("open SSE stream: %w", err))
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := readLimited(response.Body, 16*1024)
		c.failStream(generation, fmt.Errorf("open SSE stream: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data))))
		return
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != "text/event-stream" {
		c.failStream(generation, fmt.Errorf("legacy SSE endpoint returned %q", mediaType))
		return
	}
	reader := bufio.NewReader(response.Body)
	readySignaled := false
	for {
		event, err := ReadSSE(reader, c.opts.MaxResponseBytes)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return
			}
			c.failStream(generation, fmt.Errorf("SSE stream ended: %w", err))
			return
		}
		if len(event.Data) == 0 {
			continue
		}
		if event.Event == "endpoint" || (!readySignaled && looksLikeEndpoint(string(event.Data))) {
			endpoint, err := resolveEndpoint(c.opts.URL, string(event.Data))
			if err != nil {
				c.failStream(generation, err)
				return
			}
			c.mu.Lock()
			if generation == c.generation && !c.closed {
				c.endpoint = endpoint
			}
			ready := c.ready
			c.mu.Unlock()
			if !readySignaled {
				readySignaled = true
				select {
				case ready <- nil:
				default:
					{
					}
				}
			}
			continue
		}
		request, rpcResponse, err := mcp.ParseEnvelope(event.Data)
		if err != nil {
			c.opts.Logger.Debug("ignoring invalid legacy SSE message", "error", err)
			continue
		}
		if rpcResponse != nil {
			c.deliver(rpcResponse)
			continue
		}
		if request != nil && !request.ID.IsZero() {
			payload, _ := mcp.NewError(request.ID, -32601, "client-side MCP method is not supported by this gateway", nil)
			c.mu.Lock()
			endpoint := c.endpoint
			c.mu.Unlock()
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), c.opts.Timeout)
				defer cancel()
				_ = c.post(ctx, endpoint, payload, nil)
			}()
		}
	}
}
func (c *LegacySSEClient) post(ctx context.Context, endpoint string, payload []byte, id mcp.ID) error {
	if endpoint == "" {
		return errors.New("legacy SSE server did not provide a POST endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range c.opts.Headers {
		req.Header.Set(k, v)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("legacy SSE POST: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := readLimited(response.Body, 16*1024)
		return fmt.Errorf("legacy SSE POST HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	if response.StatusCode == http.StatusAccepted || response.ContentLength == 0 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "application/json" && id != nil {
		data, err := readLimited(response.Body, c.opts.MaxResponseBytes)
		if err != nil {
			return err
		}
		_, rpcResponse, err := mcp.ParseEnvelope(data)
		if err != nil {
			return err
		}
		if rpcResponse != nil {
			c.deliver(rpcResponse)
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	}
	return nil
}
func (c *LegacySSEClient) deliver(response *mcp.Response) {
	key := response.ID.String()
	c.mu.Lock()
	ch := c.pending[key]
	delete(c.pending, key)
	c.mu.Unlock()
	if ch == nil {
		return
	}
	if response.Error != nil {
		ch <- callResult{err: response.Error}
	} else {
		ch <- callResult{result: response.Result}
	}
}
func (c *LegacySSEClient) removePending(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}
func (c *LegacySSEClient) failStream(generation uint64, err error) {
	c.mu.Lock()
	if generation != c.generation {
		c.mu.Unlock()
		return
	}
	c.running = false
	c.endpoint = ""
	ready := c.ready
	pending := c.pending
	c.pending = map[string]chan callResult{}
	c.cancel = nil
	c.mu.Unlock()
	select {
	case ready <- err:
	default:
		{
		}
	}
	for _, ch := range pending {
		ch <- callResult{err: err}
	}
	c.opts.Logger.Warn("legacy SSE stream disconnected", "error", err)
}
func resolveEndpoint(base, endpoint string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	target, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", err
	}
	return baseURL.ResolveReference(target).String(), nil
}
func looksLikeEndpoint(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "/")
}
