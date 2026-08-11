package config

import (
	"context"
	"os"
	"sync"
	"time"
)

type Watcher struct {
	path     string
	interval time.Duration
	mu       sync.Mutex
	lastMod  time.Time
	lastSize int64
}

func NewWatcher(path string, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = time.Second
	}
	return &Watcher{path: path, interval: interval}
}

func (w *Watcher) Run(ctx context.Context, onChange func(Config, error)) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	w.check(onChange)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.check(onChange)
		}
	}
}

func (w *Watcher) check(onChange func(Config, error)) {
	info, err := os.Stat(w.path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		onChange(Config{}, err)
		return
	}
	w.mu.Lock()
	changed := info.ModTime() != w.lastMod || info.Size() != w.lastSize
	if changed {
		w.lastMod, w.lastSize = info.ModTime(), info.Size()
	}
	w.mu.Unlock()
	if !changed {
		return
	}
	cfg, err := Load(w.path)
	onChange(cfg, err)
}
