package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

const (
	refreshDebounce = 10 * time.Millisecond

	// failedDiscoveryTTL is how long a failed discovery is reused. Long enough to
	// absorb a client's immediate retries, including the fallback from
	// server/discover to initialize, without rediscovering every component each
	// time; short enough that a recovered component is picked up quickly.
	failedDiscoveryTTL = 5 * time.Second
)

var (

	// ErrCatalogUnavailable means a search-enabled catalog could not refresh.
	ErrCatalogUnavailable = fmt.Errorf("CATALOG_UNAVAILABLE: tool catalog refresh failed; retry")
)

// Registry caches immutable catalogs by complete configuration fingerprint.
type Registry struct {
	discoverer component.Discoverer
	ctx        context.Context
	cancel     context.CancelFunc

	// compiles counts detached initial compiles and notification refreshes so
	// Close can wait for them.
	// Add happens under mu only while the registry is open, never after Close
	// marks it closed, so it cannot race Close's Wait.
	compiles sync.WaitGroup

	mu      sync.Mutex
	entries map[string]*registryEntry
	closed  bool

	// failureTTL is failedDiscoveryTTL; tests shorten it.
	failureTTL time.Duration

	// kept holds, by fingerprint, the components that discovered successfully in
	// a compile that ended needing authorization, so a request that rediscovers
	// with its own credentials only reaches the components that challenged.
	kept map[string]*keptDiscovery

	// invalidations counts InvalidateComponent calls, so a compile that started
	// before one does not keep features it may have discarded.
	invalidations uint64
}

type keptDiscovery struct {
	config   *config.Config
	features map[string]*component.Features
}

// keepingDiscoverer serves kept components and records the ones it discovers.
type keepingDiscoverer struct {
	discoverer component.Discoverer
	kept       map[string]*component.Features

	mu    sync.Mutex
	found map[string]*component.Features
}

type registryEntry struct {
	ready       chan struct{}
	catalog     *Catalog
	err         error
	config      *config.Config
	refreshing  bool
	pending     bool
	timer       *time.Timer
	callbacks   []func(bool)
	stale       bool
	refreshDone chan struct{}

	// failedAt is when a cached discovery failure was recorded. A failed entry
	// is replaced by the first request after failureTTL.
	failedAt time.Time
}

// NewRegistry creates a catalog registry.
func NewRegistry(discoverer component.Discoverer) *Registry {
	ctx, cancel := context.WithCancel(context.Background())
	return &Registry{
		discoverer: discoverer,
		ctx:        ctx,
		cancel:     cancel,
		entries:    make(map[string]*registryEntry),
		failureTTL: failedDiscoveryTTL,
		kept:       make(map[string]*keptDiscovery),
	}
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

// Cached returns a completed, current catalog without starting discovery or
// waiting for compilation. Authorization failures and pending catalogs stay request-local.
func (r *Registry) Cached(cfg *config.Config) (*Catalog, bool) {
	fingerprint, err := Fingerprint(cfg)
	if err != nil {
		return nil, false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[fingerprint]
	if r.closed || entry == nil || entry.stale || entry.err != nil {
		return nil, false
	}

	select {
	case <-entry.ready:
		return entry.catalog, entry.catalog != nil && len(entry.catalog.pendingAuthorization) == 0
	default:
		return nil, false
	}
}

// Get returns the cached catalog for cfg, compiling it once on first use.
func (r *Registry) Get(ctx context.Context, cfg *config.Config) (*Catalog, string, error) {
	fingerprint, err := Fingerprint(cfg)
	if err != nil {
		return nil, "", err
	}

	entry, created := r.entryForConfig(fingerprint, cfg)
	if created {
		// Discovery outlives the request that started it: a client that times out
		// and retries must find the work in progress (or finished), not restart it.
		// Request values, including the headers that authenticate component
		// connections, are retained; only cancellation is detached.
		go func() {
			defer r.compiles.Done()
			r.compileEntry(context.WithoutCancel(ctx), fingerprint, cfg, entry)
		}()
	}

	compiled, err := r.waitForCatalog(ctx, fingerprint, entry, cfg.ToolSearch)
	if !created && needsAuthorization(compiled, err) {
		// That compile used another request's credentials; discover once with this
		// one's. Re-entering the cache would retry once per concurrent pending request.
		compiled, err = r.compileKeeping(ctx, fingerprint, cfg)
		if err == nil {
			r.publish(fingerprint, cfg, compiled)
		}
	}

	return compiled, fingerprint, err
}

// compileEntry performs the initial compile for entry and releases its waiters.
// It is bounded by each component's discovery timeout and by Close, not by any
// caller: Close cancels it and waits for it, so this compile does not outlive the
// registry's owner closing the component factory. Compiles that run on a caller's
// context (Refresh, a waiter's recompile) are that caller's to cancel and wait for.
func (r *Registry) compileEntry(ctx context.Context, fingerprint string, cfg *config.Config, entry *registryEntry) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(r.ctx, cancel)
	defer stop()

	compiled, compileErr := r.compileKeeping(ctx, fingerprint, cfg)
	r.mu.Lock()
	defer r.mu.Unlock()
	entry.catalog, entry.err = compiled, compileErr

	// Pending catalogs and authorization failures are not cached: they depend on
	// the caller's credentials, so the next request rediscovers with its own. A
	// ready catalog is shared by every caller with this configuration, so users
	// who have not signed in see its tools and get 401s. Other failures are shared
	// briefly, so retries in quick succession do not each rediscover everything.
	// An invalidated in-flight compile must not evict or publish over its replacement.
	if r.entries[fingerprint] == entry {
		switch {
		case compileErr != nil && len(component.AuthorizationErrors(compileErr)) == 0 && !r.closed:
			entry.failedAt = time.Now()
		case compileErr != nil || len(compiled.pendingAuthorization) > 0:
			delete(r.entries, fingerprint)
		}
	}

	// Callers still receive an invalidated successful compile, so its index must be ready too.
	if compileErr == nil && !r.closed {
		compiled.StartSearchIndex(r.ctx)
	}

	close(entry.ready)
}

// needsAuthorization reports whether a compile ended waiting on a component's
// authorization, as an error or as a pending catalog.
func needsAuthorization(compiled *Catalog, err error) bool {
	return len(component.AuthorizationErrors(err)) > 0 || err == nil && len(compiled.pendingAuthorization) > 0
}

// compileKeeping compiles cfg, reusing components kept from an earlier compile
// that ended needing authorization. Only the components that challenged then are
// discovered again, with this request's credentials.
//
// A kept component is shared by every request with this configuration, as a
// ready catalog is: one discovered with a user's credentials is reused for a
// user without them, who gets its 401 on the call instead of during discovery.
func (r *Registry) compileKeeping(ctx context.Context, fingerprint string, cfg *config.Config) (*Catalog, error) {
	r.mu.Lock()
	discoverer := &keepingDiscoverer{discoverer: r.discoverer, found: make(map[string]*component.Features)}
	if kept := r.kept[fingerprint]; kept != nil {
		// Never mutated once stored, so it is read without the lock.
		discoverer.kept = kept.features
	}
	invalidations := r.invalidations
	r.mu.Unlock()

	compiled, err := compile(ctx, cfg, discoverer, cfg.DeferAuthorization)

	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.closed:
	case !needsAuthorization(compiled, err):
		// A ready catalog is cached now and replaces the kept components; any
		// other failure leaves them, since they did discover successfully.
		if err == nil {
			delete(r.kept, fingerprint)
		}
	case r.invalidations == invalidations:
		features := make(map[string]*component.Features, len(discoverer.kept)+len(discoverer.found))
		maps.Copy(features, discoverer.kept)
		maps.Copy(features, discoverer.found)
		r.kept[fingerprint] = &keptDiscovery{config: cfg, features: features}
	}
	return compiled, err
}

// serverKey identifies a component by its whole configuration: programmatic
// configurations may give distinct components the same name.
func serverKey(server config.Server) string {
	// A config.Server always marshals.
	key, _ := json.Marshal(server)
	return string(key)
}

func (d *keepingDiscoverer) Discover(ctx context.Context, server config.Server) (*component.Features, error) {
	if features, ok := d.kept[serverKey(server)]; ok {
		// Compiling appends configured resources to these slices; clipping makes
		// that append copy instead of writing into the shared backing array.
		clone := *features
		clone.Tools = slices.Clip(clone.Tools)
		clone.Prompts = slices.Clip(clone.Prompts)
		clone.Resources = slices.Clip(clone.Resources)
		clone.ResourceTemplates = slices.Clip(clone.ResourceTemplates)
		return &clone, nil
	}
	features, err := d.discoverer.Discover(ctx, server)
	if err == nil && features != nil {
		d.mu.Lock()
		d.found[serverKey(server)] = features
		d.mu.Unlock()
	}
	return features, err
}

// publish caches a ready catalog a waiter discovered for itself, the same as a first
// compile would, unless another entry has taken its place since.
func (r *Registry) publish(fingerprint string, cfg *config.Config, compiled *Catalog) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	compiled.StartSearchIndex(r.ctx)
	if len(compiled.pendingAuthorization) > 0 || r.entries[fingerprint] != nil {
		return
	}
	ready := make(chan struct{})
	close(ready)
	r.entries[fingerprint] = &registryEntry{ready: ready, catalog: compiled, config: cfg}
}

// entryForConfig returns the entry for fingerprint, creating it for the caller
// to compile if needed. A closed registry starts no compile: it returns an
// uncached entry already failed with context.Canceled: Close has already waited
// for the compiles started before it, so one started now could outlive the factory.
func (r *Registry) entryForConfig(fingerprint string, cfg *config.Config) (entry *registryEntry, created bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		entry = &registryEntry{ready: make(chan struct{}), config: cfg, err: context.Canceled}
		close(entry.ready)
		return entry, false
	}
	if entry := r.entries[fingerprint]; entry != nil && !r.failureExpired(entry) {
		return entry, false
	}
	entry = &registryEntry{ready: make(chan struct{}), config: cfg}
	r.entries[fingerprint] = entry
	r.compiles.Add(1)
	return entry, true
}

// failureExpired reports whether entry holds a cached failure older than
// failureTTL, which the next request replaces with a new discovery. The caller holds r.mu.
func (r *Registry) failureExpired(entry *registryEntry) bool {
	return entry.err != nil && !entry.failedAt.IsZero() && time.Since(entry.failedAt) >= r.failureTTL
}

// waitForCatalog waits for initial compilation and any required search refresh
// without holding the registry mutex.
func (r *Registry) waitForCatalog(ctx context.Context, fingerprint string, entry *registryEntry, toolSearch bool) (*Catalog, error) {
	select {
	case <-entry.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	compiled, done, err := func() (*Catalog, <-chan struct{}, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if entry.err != nil || !entry.stale || !toolSearch {
			return entry.catalog, nil, entry.err
		}
		if r.closed {
			return nil, nil, context.Canceled
		}
		if entry.refreshDone == nil {
			entry.refreshDone = make(chan struct{})
		}
		if !entry.refreshing && entry.timer == nil {
			entry.timer = time.AfterFunc(refreshDebounce, func() { r.runRefresh(fingerprint) })
		}
		return nil, entry.refreshDone, nil
	}()
	if done == nil {
		return compiled, err
	}
	select {
	case <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.entries[fingerprint]
	if current == nil || current.stale {
		return nil, ErrCatalogUnavailable
	}
	return current.catalog, current.err
}

// Refresh recompiles cfg and replaces its cached catalog only after success.
func (r *Registry) Refresh(ctx context.Context, cfg *config.Config) (*Catalog, string, error) {
	fingerprint, err := Fingerprint(cfg)
	if err != nil {
		return nil, "", err
	}
	var current *registryEntry

	// Search catalogs must be marked stale before discovery so Get waits for this
	// refresh. Wait for any initial compile or earlier refresh to finish first.
	if cfg.ToolSearch {
		for {
			done, err := func() (<-chan struct{}, error) {
				r.mu.Lock()
				defer r.mu.Unlock()
				if r.closed {
					return nil, context.Canceled
				}
				current = r.entries[fingerprint]
				if current == nil {
					return nil, nil
				}
				select {
				case <-current.ready:
					if current.refreshDone != nil {
						return current.refreshDone, nil
					}
					current.stale = true
					current.refreshing = true
					current.refreshDone = make(chan struct{})
					return nil, nil
				default:
					return current.ready, nil
				}
			}()
			if err != nil {
				return nil, fingerprint, err
			}
			if done == nil {
				break
			}
			select {
			case <-done:
			case <-ctx.Done():
				return nil, fingerprint, ctx.Err()
			}
		}
	}

	// Refreshes never defer: a 401 keeps the installed snapshot.
	compiled, err := compile(ctx, cfg, r.discoverer, false)

	// Complete the search refresh on the same entry that Get is waiting on.
	// Keep it stale after failure or while a notification refresh is pending.
	if cfg.ToolSearch && current != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.closed {
			return nil, fingerprint, context.Canceled
		}
		r.finishRefresh(fingerprint, current, compiled, err)
		return compiled, fingerprint, err
	}
	if err != nil {
		if cfg.ToolSearch {
			r.mu.Lock()
			defer r.mu.Unlock()
			if entry := r.entries[fingerprint]; entry != nil {
				entry.stale = true
			}
		}
		return nil, fingerprint, err
	}
	entry := &registryEntry{
		ready:   make(chan struct{}),
		catalog: compiled,
		config:  cfg,
	}
	close(entry.ready)
	compiled.StartSearchIndex(r.ctx)
	old := func() *registryEntry {
		r.mu.Lock()
		defer r.mu.Unlock()
		old := r.entries[fingerprint]
		r.entries[fingerprint] = entry
		if old != nil && old.refreshDone != nil {
			close(old.refreshDone)
			old.refreshDone = nil
		}
		return old
	}()
	if old != nil {
		old.catalog.StopSearchIndex()
	}
	return compiled, fingerprint, nil
}

// InvalidateComponent drops all discovery views containing a component. Rediscovery
// happens on the next request with that request's credentials, not in a background probe.
func (r *Registry) InvalidateComponent(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.invalidations++
	for fingerprint, kept := range r.kept {
		if slices.ContainsFunc(kept.config.Servers, func(server config.Server) bool { return server.Name == name }) {
			delete(r.kept, fingerprint)
		}
	}

	for fingerprint, entry := range r.entries {
		if entry.config == nil {
			continue
		}

		for _, server := range entry.config.Servers {
			if server.Name != name {
				continue
			}

			delete(r.entries, fingerprint)
			entry.catalog.StopSearchIndex()
			if entry.timer != nil {
				entry.timer.Stop()
			}

			if entry.refreshDone != nil {
				close(entry.refreshDone)
				entry.refreshDone = nil
			}

			break
		}
	}
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
	if entry.config.ToolSearch {
		entry.stale = true
		if entry.refreshDone == nil {
			entry.refreshDone = make(chan struct{})
		}
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
	// Counted like an initial compile: Close cancels it through r.ctx and waits.
	r.compiles.Add(1)
	r.mu.Unlock()

	compiled, err := compile(r.ctx, cfg, r.discoverer, false)

	r.mu.Lock()
	if r.entries[fingerprint] == entry {
		r.finishRefresh(fingerprint, entry, compiled, err)
	}
	r.mu.Unlock()

	// The refresh is done once its result is installed. Callbacks run outside the
	// count: one that calls Close would otherwise wait for itself and deadlock.
	r.compiles.Done()
	for _, callback := range callbacks {
		callback(err == nil)
	}
}

// finishRefresh installs a successful snapshot and releases Get waiters once
// no further notification refresh is pending. The caller holds r.mu.
func (r *Registry) finishRefresh(fingerprint string, entry *registryEntry, compiled *Catalog, err error) {
	if err == nil {
		compiled.StartSearchIndex(r.ctx)
		old := entry.catalog
		entry.catalog = compiled
		entry.err = nil
		old.StopSearchIndex()
	}
	entry.refreshing = false
	pending := entry.pending
	entry.pending = false
	if entry.config.ToolSearch {
		entry.stale = pending || err != nil
		if !pending && entry.refreshDone != nil {
			close(entry.refreshDone)
			entry.refreshDone = nil
		}
	}
	if pending && !r.closed {
		entry.timer = time.AfterFunc(refreshDebounce, func() { r.runRefresh(fingerprint) })
	}
}

// Close cancels pending catalog refreshes.
func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	r.cancel()
	clear(r.kept)
	for _, entry := range r.entries {
		entry.catalog.StopSearchIndex()
		if entry.refreshDone != nil {
			close(entry.refreshDone)
			entry.refreshDone = nil
		}
		if entry.timer != nil {
			entry.timer.Stop()
			entry.timer = nil
		}
	}
	r.mu.Unlock()
	// Initial compiles and notification refreshes are cancelled by r.cancel
	// above; wait so none still discovers once Close returns.
	r.compiles.Wait()
}

func canonicalConfig(cfg config.Config) config.Config {
	if cfg.Servers == nil {
		cfg.Servers = []config.Server{}
	} else {
		cfg.Servers = slices.Clone(cfg.Servers)
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
