package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sid-pani/lessen-mcp-gateway/internal/mcp"
)

type StdioOptions struct {
	Command          string
	Args             []string
	Env              map[string]string
	WorkingDir       string
	Persistent       bool
	IdleTimeout      time.Duration
	MaxResponseBytes int64
	Logger           *slog.Logger
}

type callResult struct {
	result json.RawMessage
	err    error
}

type StdioClient struct {
	opts StdioOptions

	mu         sync.Mutex
	writeMu    sync.Mutex
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	pending    map[string]chan callResult
	closed     bool
	generation uint64
	idleTimer  *time.Timer
	startedAt  time.Time
	nextID     atomic.Uint64
}

func NewStdio(opts StdioOptions) (*StdioClient, error) {
	if opts.Command == "" {
		return nil, errors.New("stdio command is required")
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
	return &StdioClient{opts: opts, pending: map[string]chan callResult{}}, nil
}

func (c *StdioClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := c.ensureStarted(ctx); err != nil {
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
		return nil, errors.New("stdio client is closed")
	}
	c.pending[key] = ch
	c.mu.Unlock()
	if err := c.write(payload); err != nil {
		c.removePending(key)
		return nil, err
	}
	defer c.touch()
	select {
	case <-ctx.Done():
		c.removePending(key)
		return nil, ctx.Err()
	case outcome := <-ch:
		return outcome.result, outcome.err
	}
}

func (c *StdioClient) Notify(ctx context.Context, method string, params any) error {
	if err := c.ensureStarted(ctx); err != nil {
		return err
	}
	payload, err := mcp.NewNotification(method, params)
	if err != nil {
		return err
	}
	defer c.touch()
	return c.write(payload)
}

func (c *StdioClient) Health(ctx context.Context) error {
	_, err := c.Call(ctx, "ping", map[string]any{})
	return err
}

func (c *StdioClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	if c.idleTimer != nil {
		c.idleTimer.Stop()
		c.idleTimer = nil
	}
	cmd := c.cmd
	stdin := c.stdin
	c.cmd = nil
	c.stdin = nil
	pending := c.pending
	c.pending = map[string]chan callResult{}
	c.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	for _, ch := range pending {
		ch <- callResult{err: errors.New("stdio client closed")}
	}
	return nil
}

func (c *StdioClient) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cmd != nil && c.cmd.Process != nil
}

func (c *StdioClient) StartedAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.startedAt
}

func (c *StdioClient) ensureStarted(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("stdio client is closed")
	}
	if c.cmd != nil {
		return nil
	}

	cmd := exec.Command(c.opts.Command, c.opts.Args...)
	cmd.Dir = c.opts.WorkingDir
	cmd.Env = append([]string{}, os.Environ()...)
	for key, value := range c.opts.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open stdio stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("open stdio stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fmt.Errorf("open stdio stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return fmt.Errorf("start stdio server: %w", err)
	}
	c.generation++
	generation := c.generation
	c.cmd = cmd
	c.stdin = stdin
	c.startedAt = time.Now().UTC()
	c.opts.Logger.Info("stdio server started", "pid", cmd.Process.Pid)
	go c.readLoop(generation, stdout)
	go c.stderrLoop(generation, stderr)
	go func() {
		err := cmd.Wait()
		c.handleExit(generation, err)
	}()
	c.resetIdleLocked()
	return nil
}

func (c *StdioClient) write(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	stdin := c.stdin
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return errors.New("stdio client is closed")
	}
	if stdin == nil {
		return errors.New("stdio server is not running")
	}
	payload = append(payload, '\n')
	_, err := stdin.Write(payload)
	if err != nil {
		return fmt.Errorf("write stdio request: %w", err)
	}
	return nil
}

func (c *StdioClient) readLoop(generation uint64, stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	max := int(c.opts.MaxResponseBytes)
	if max < 64*1024 {
		max = 64 * 1024
	}
	scanner.Buffer(make([]byte, 64*1024), max)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		request, response, err := mcp.ParseEnvelope(line)
		if err != nil {
			c.opts.Logger.Warn("discarding invalid stdio message", "error", err, "bytes", len(line))
			continue
		}
		if response != nil {
			c.deliver(response)
			continue
		}
		if request != nil && !request.ID.IsZero() {
			payload, _ := mcp.NewError(request.ID, -32601, "client-side MCP method is not supported by this gateway", nil)
			_ = c.write(payload)
		}
	}
	if err := scanner.Err(); err != nil {
		c.opts.Logger.Warn("stdio read loop ended", "error", err)
	}
	c.handleStreamClosed(generation)
}

func (c *StdioClient) stderrLoop(generation uint64, stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 4096), 256*1024)
	for scanner.Scan() {
		c.mu.Lock()
		current := generation == c.generation
		c.mu.Unlock()
		if !current {
			return
		}
		text := scanner.Text()
		if len(text) > 4096 {
			text = text[:4096] + "…"
		}
		c.opts.Logger.Debug("stdio backend", "message", text)
	}
}

func (c *StdioClient) deliver(response *mcp.Response) {
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
		return
	}
	ch <- callResult{result: response.Result}
}

func (c *StdioClient) removePending(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}

func (c *StdioClient) handleStreamClosed(generation uint64) {
	c.mu.Lock()
	if generation != c.generation {
		c.mu.Unlock()
		return
	}
	cmd := c.cmd
	c.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (c *StdioClient) handleExit(generation uint64, err error) {
	c.mu.Lock()
	if generation != c.generation {
		c.mu.Unlock()
		return
	}
	c.cmd = nil
	c.stdin = nil
	c.startedAt = time.Time{}
	pending := c.pending
	c.pending = map[string]chan callResult{}
	if c.idleTimer != nil {
		c.idleTimer.Stop()
		c.idleTimer = nil
	}
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		if err != nil {
			c.opts.Logger.Warn("stdio server exited", "error", err)
		} else {
			c.opts.Logger.Info("stdio server exited")
		}
	}
	exitErr := err
	if exitErr == nil {
		exitErr = errors.New("stdio server exited")
	}
	for _, ch := range pending {
		ch <- callResult{err: exitErr}
	}
}

func (c *StdioClient) touch() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetIdleLocked()
}

func (c *StdioClient) resetIdleLocked() {
	if c.opts.Persistent || c.closed || c.cmd == nil {
		return
	}
	if c.idleTimer != nil {
		c.idleTimer.Stop()
	}
	generation := c.generation
	c.idleTimer = time.AfterFunc(c.opts.IdleTimeout, func() { c.stopIdle(generation) })
}

func (c *StdioClient) stopIdle(generation uint64) {
	c.mu.Lock()
	if c.closed || c.opts.Persistent || generation != c.generation || c.cmd == nil || len(c.pending) > 0 {
		c.mu.Unlock()
		return
	}
	cmd := c.cmd
	stdin := c.stdin
	c.cmd = nil
	c.stdin = nil
	c.startedAt = time.Time{}
	c.idleTimer = nil
	c.generation++
	c.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	c.opts.Logger.Info("stdio server stopped after idle timeout")
}

func (c *StdioClient) SessionGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}
