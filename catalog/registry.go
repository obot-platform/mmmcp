package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

const (
	refreshDebounce = 10 * time.Millisecond
)

// Registry caches immutable catalogs by complete configuration fingerprint.
type Registry struct {
	discoverer component.Discoverer

	mu      sync.Mutex
	entries map[string]*registryEntry
	closed  bool
}

type registryEntry struct {
	ready      chan struct{}
	catalog    *Catalog
	err        error
	config     *config.Config
	refreshing bool
	pending    bool
	timer      *time.Timer
	callbacks  []func(bool)
}

// NewRegistry creates a catalog registry.
func NewRegistry(discoverer component.Discoverer) *Registry {
	return &Registry{discoverer: discoverer, entries: make(map[string]*registryEntry)}
}

// Fingerprint returns a stable, non-reversible digest of a complete configuration.
func Fingerprint(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("catalog: nil config")
	}
	canonical := canonicalConfig(*cfg)
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("catalog fingerprint: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Get returns the cached catalog for cfg, compiling it once on first use.
func (r *Registry) Get(ctx context.Context, cfg *config.Config) (*Catalog, string, error) {
	fingerprint, err := Fingerprint(cfg)
	if err != nil {
		return nil, "", err
	}

	r.mu.Lock()
	if entry := r.entries[fingerprint]; entry != nil {
		r.mu.Unlock()
		select {
		case <-entry.ready:
			r.mu.Lock()
			catalog, entryErr := entry.catalog, entry.err
			r.mu.Unlock()
			return catalog, fingerprint, entryErr
		case <-ctx.Done():
			return nil, fingerprint, ctx.Err()
		}
	}
	entry := &registryEntry{ready: make(chan struct{}), config: cfg}
	r.entries[fingerprint] = entry
	r.mu.Unlock()

	entry.catalog, entry.err = Compile(ctx, cfg, r.discoverer)
	r.mu.Lock()
	if entry.err != nil {
		delete(r.entries, fingerprint)
	}
	close(entry.ready)
	r.mu.Unlock()
	return entry.catalog, fingerprint, entry.err
}

// Refresh recompiles cfg and replaces its cached catalog only after success.
func (r *Registry) Refresh(ctx context.Context, cfg *config.Config) (*Catalog, string, error) {
	fingerprint, err := Fingerprint(cfg)
	if err != nil {
		return nil, "", err
	}
	compiled, err := Compile(ctx, cfg, r.discoverer)
	if err != nil {
		return nil, fingerprint, err
	}
	entry := &registryEntry{ready: make(chan struct{}), catalog: compiled, config: cfg}
	close(entry.ready)
	r.mu.Lock()
	r.entries[fingerprint] = entry
	r.mu.Unlock()
	return compiled, fingerprint, nil
}

// RequestRefresh debounces a full rediscovery for fingerprint. A failed
// refresh leaves the previous immutable snapshot installed.
func (r *Registry) RequestRefresh(fingerprint string, callback func(bool)) {
	r.mu.Lock()
	entry := r.entries[fingerprint]
	if r.closed || entry == nil || entry.config == nil {
		r.mu.Unlock()
		return
	}
	if callback != nil {
		entry.callbacks = append(entry.callbacks, callback)
	}
	if entry.refreshing {
		entry.pending = true
		r.mu.Unlock()
		return
	}
	if entry.timer == nil {
		entry.timer = time.AfterFunc(refreshDebounce, func() { r.runRefresh(fingerprint) })
	} else {
		entry.timer.Reset(refreshDebounce)
	}
	r.mu.Unlock()
}

func (r *Registry) runRefresh(fingerprint string) {
	r.mu.Lock()
	entry := r.entries[fingerprint]
	if r.closed || entry == nil || entry.refreshing {
		r.mu.Unlock()
		return
	}
	entry.timer = nil
	entry.refreshing = true
	entry.pending = false
	cfg := entry.config
	callbacks := entry.callbacks
	entry.callbacks = nil
	r.mu.Unlock()

	compiled, err := Compile(context.Background(), cfg, r.discoverer)

	r.mu.Lock()
	entry = r.entries[fingerprint]
	if entry == nil {
		r.mu.Unlock()
		return
	}
	if err == nil {
		entry.catalog = compiled
		entry.err = nil
	}
	entry.refreshing = false
	pending := entry.pending
	entry.pending = false
	if pending && !r.closed {
		entry.timer = time.AfterFunc(refreshDebounce, func() { r.runRefresh(fingerprint) })
	}
	r.mu.Unlock()
	for _, callback := range callbacks {
		callback(err == nil)
	}
}

// Close cancels pending catalog refreshes.
func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	for _, entry := range r.entries {
		if entry.timer != nil {
			entry.timer.Stop()
			entry.timer = nil
		}
	}
	r.mu.Unlock()
}

func canonicalConfig(cfg config.Config) config.Config {
	if cfg.Servers == nil {
		cfg.Servers = []config.Server{}
	} else {
		cfg.Servers = append([]config.Server(nil), cfg.Servers...)
	}
	for i := range cfg.Servers {
		server := &cfg.Servers[i]
		if server.Headers == nil {
			server.Headers = map[string]string{}
		}
		if server.PassthroughHeaders == nil {
			server.PassthroughHeaders = []string{}
		}
		if server.Args == nil {
			server.Args = []string{}
		}
		if server.Env == nil {
			server.Env = map[string]string{}
		}
		if server.Tools == nil {
			server.Tools = []config.ToolOverride{}
		}
		if server.Prompts == nil {
			server.Prompts = []config.PromptOverride{}
		}
		if server.Resources == nil {
			server.Resources = []config.ResourceOverride{}
		}
		if server.ResourceTemplates == nil {
			server.ResourceTemplates = []config.ResourceTemplateOverride{}
		}
	}
	return cfg
}
