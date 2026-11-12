package catalog_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

type countingDiscoverer struct {
	mu      sync.Mutex
	count   int
	started chan struct{}
	release chan struct{}
	once    sync.Once
	err     error
}

type observedDoneContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

// ctxDiscoverer blocks until its context ends, recording when it returned.
type ctxDiscoverer struct {
	started  chan struct{}
	once     sync.Once
	returned atomic.Bool
}

// refreshBlockingDiscoverer answers the first discovery and blocks every later
// one until its context ends.
type refreshBlockingDiscoverer struct {
	calls           atomic.Int32
	refreshStarted  chan struct{}
	refreshReturned atomic.Bool
}

func (c *observedDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func (d *ctxDiscoverer) Discover(ctx context.Context, _ config.Server) (*component.Features, error) {
	d.once.Do(func() { close(d.started) })
	<-ctx.Done()
	d.returned.Store(true)
	return nil, ctx.Err()
}

func (d *refreshBlockingDiscoverer) Discover(ctx context.Context, _ config.Server) (*component.Features, error) {
	if d.calls.Add(1) == 1 {
		return &component.Features{Tools: []*mcp.Tool{{Name: "tool", InputSchema: map[string]any{"type": "object"}}}}, nil
	}
	close(d.refreshStarted)
	<-ctx.Done()
	d.refreshReturned.Store(true)
	return nil, ctx.Err()
}

func TestFingerprintIsStableCompleteAndSecretSafe(t *testing.T) {
	cfg := &config.Config{Servers: []config.Server{{
		Name: "fixture", URL: "https://example.invalid", Headers: map[string]string{"Authorization": "Bearer secret"},
	}}}
	first, err := catalog.Fingerprint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	equivalent := &config.Config{Servers: []config.Server{{
		Name: "fixture", URL: "https://example.invalid", Headers: map[string]string{"Authorization": "Bearer secret"}, Args: []string{}, Env: map[string]string{},
	}}}
	second, err := catalog.Fingerprint(equivalent)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equivalent fingerprints differ: %q != %q", first, second)
	}
	if strings.Contains(first, "secret") || strings.Contains(first, "example.invalid") {
		t.Fatalf("fingerprint exposes config values: %q", first)
	}
	changed := *cfg
	changed.Listen = "127.0.0.1:9000"
	third, err := catalog.Fingerprint(&changed)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("configuration change did not change fingerprint")
	}
}

func TestRegistryDiscoveryRevisionTriggersRediscovery(t *testing.T) {
	discoverer := &countingDiscoverer{started: make(chan struct{}), release: make(chan struct{})}
	close(discoverer.release)
	registry := catalog.NewRegistry(discoverer)
	defer registry.Close()
	for i, field := range []string{"", "    discoveryRevision: revision-1\n"} {
		cfg, err := config.Load([]byte("servers:\n  - name: fixture\n    url: https://example.invalid\n"+field), config.LoadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			_, _, err := registry.Get(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
		}
		if got := discoverer.Count(); got != i+1 {
			t.Fatalf("discoveries = %d, want %d", got, i+1)
		}
	}
}

func TestRegistryDeduplicatesConcurrentCompilation(t *testing.T) {
	discoverer := &countingDiscoverer{started: make(chan struct{}), release: make(chan struct{})}
	registry := catalog.NewRegistry(discoverer)
	cfg := &config.Config{Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}

	const callers = 8
	results := make(chan *catalog.Catalog, callers)
	errors := make(chan error, callers)
	for range callers {
		go func() {
			compiled, _, err := registry.Get(t.Context(), cfg)
			results <- compiled
			errors <- err
		}()
	}
	<-discoverer.started
	close(discoverer.release)

	var first *catalog.Catalog
	for range callers {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		compiled := <-results
		if first == nil {
			first = compiled
		} else if compiled != first {
			t.Fatal("registry returned distinct metadata snapshots")
		}
	}
	if got := discoverer.Count(); got != 1 {
		t.Fatalf("discoveries = %d, want 1", got)
	}
}

func TestRegistryWaitersReturnInitialCompilationError(t *testing.T) {
	failure := errors.New("discovery failed")
	discoverer := &countingDiscoverer{
		started: make(chan struct{}),
		release: make(chan struct{}),
		err:     failure,
	}
	defer func() {
		select {
		case <-discoverer.release:
		default:
			close(discoverer.release)
		}
	}()
	registry := catalog.NewRegistry(discoverer)
	defer registry.Close()
	cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{
		Name: "fixture",
		URL:  "https://example.invalid",
	}}}

	firstDone := make(chan error, 1)
	go func() {
		_, _, err := registry.Get(t.Context(), cfg)
		firstDone <- err
	}()
	<-discoverer.started

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waiterCtx := &observedDoneContext{Context: ctx, observed: make(chan struct{})}
	waiterDone := make(chan error, 1)
	go func() {
		_, _, err := registry.Get(waiterCtx, cfg)
		waiterDone <- err
	}()
	select {
	case <-waiterCtx.observed:
	case <-time.After(time.Second):
		t.Fatal("second caller did not wait for initial compilation")
	}
	close(discoverer.release)

	for _, done := range []<-chan error{firstDone, waiterDone} {
		select {
		case err := <-done:
			if !errors.Is(err, failure) {
				t.Fatalf("initial compilation returned %v, want %v", err, failure)
			}
		case <-time.After(time.Second):
			t.Fatal("caller remained blocked after initial compilation failed")
		}
	}
	if got := discoverer.Count(); got != 1 {
		t.Fatalf("discoveries = %d, want 1", got)
	}
}

func TestRegistryCloseDuringInitialCompilation(t *testing.T) {
	const attempts = 32
	for range attempts {
		discoverer := &countingDiscoverer{started: make(chan struct{}), release: make(chan struct{})}
		registry := catalog.NewRegistry(discoverer)
		cfg := &config.Config{
			ToolSearch: true,
			Servers:    []config.Server{{Name: "fixture", URL: "https://example.invalid"}},
		}

		getDone := make(chan error, 1)
		go func() {
			_, _, err := registry.Get(t.Context(), cfg)
			getDone <- err
		}()
		<-discoverer.started

		closeDone := make(chan struct{})
		go func() {
			registry.Close()
			close(closeDone)
		}()
		close(discoverer.release)

		select {
		case err := <-getDone:
			// Close cancels the detached compile, so it may lose the race to finish.
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("initial compilation after Close: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("initial compilation did not finish")
		}
		select {
		case <-closeDone:
		case <-time.After(time.Second):
			t.Fatal("Close did not finish")
		}
	}
}

func (d *countingDiscoverer) Discover(context.Context, config.Server) (*component.Features, error) {
	d.mu.Lock()
	d.count++
	d.mu.Unlock()
	d.once.Do(func() { close(d.started) })
	<-d.release
	if d.err != nil {
		return nil, d.err
	}
	return &component.Features{Tools: []*mcp.Tool{{Name: "tool", InputSchema: map[string]any{"type": "object"}}}}, nil
}

func (d *countingDiscoverer) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.count
}

// A caller that gives up must not cancel or fail the shared discovery: a client
// that times out and retries has to find the work it started.
func TestRegistryInitialCompilationOutlivesCaller(t *testing.T) {
	discoverer := &countingDiscoverer{started: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(discoverer.release) })
	registry := catalog.NewRegistry(discoverer)
	// countingDiscoverer ignores cancellation, and Close waits for the compile it
	// is blocking. Release it first so a failed assertion reports instead of hanging.
	t.Cleanup(func() {
		release()
		registry.Close()
	})
	cfg := &config.Config{Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}

	ctx, cancel := context.WithCancel(t.Context())
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := registry.Get(ctx, cfg)
		firstDone <- err
	}()
	<-discoverer.started
	cancel()

	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("abandoned caller error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("abandoned caller did not return")
	}

	retryDone := make(chan error, 1)
	go func() {
		_, _, err := registry.Get(t.Context(), cfg)
		retryDone <- err
	}()
	release()

	select {
	case err := <-retryDone:
		if err != nil {
			t.Fatalf("retry after abandoned caller: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry did not finish")
	}
	if count := discoverer.Count(); count != 1 {
		t.Fatalf("discoveries = %d, want 1 shared discovery", count)
	}
	if _, ok := registry.Cached(cfg); !ok {
		t.Fatal("completed discovery was not cached")
	}
}

// Close must cancel a detached initial compile whose caller has gone, and must
// not return while it still runs: its owner closes the component factory next.
func TestRegistryCloseCancelsDetachedCompilation(t *testing.T) {
	discoverer := &ctxDiscoverer{started: make(chan struct{})}
	registry := catalog.NewRegistry(discoverer)
	cfg := &config.Config{Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}

	ctx, cancel := context.WithCancel(t.Context())
	go func() { _, _, _ = registry.Get(ctx, cfg) }()
	<-discoverer.started
	cancel()

	closeDone := make(chan struct{})
	go func() {
		registry.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the detached compile")
	}
	if !discoverer.returned.Load() {
		t.Fatal("Close returned before the detached compile finished")
	}
}

// A closed registry must not start a discovery: Close has already waited for
// the compiles started before it, and would not wait for one started after.
func TestRegistryGetAfterCloseDoesNotDiscover(t *testing.T) {
	discoverer := &countingDiscoverer{started: make(chan struct{}), release: make(chan struct{})}
	close(discoverer.release)
	registry := catalog.NewRegistry(discoverer)
	registry.Close()
	cfg := &config.Config{Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}

	if _, _, err := registry.Get(t.Context(), cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get after Close error = %v, want context.Canceled", err)
	}
	if count := discoverer.Count(); count != 0 {
		t.Fatalf("discoveries after Close = %d, want 0", count)
	}
}

// A notification refresh runs on the registry's own context, so Close must
// cancel it and wait for it, like a detached initial compile.
func TestRegistryCloseWaitsForNotificationRefresh(t *testing.T) {
	discoverer := &refreshBlockingDiscoverer{refreshStarted: make(chan struct{})}
	registry := catalog.NewRegistry(discoverer)
	cfg := &config.Config{Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}
	_, fingerprint, err := registry.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	registry.RequestRefresh(fingerprint, nil)
	select {
	case <-discoverer.refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("notification refresh did not start")
	}

	closeDone := make(chan struct{})
	go func() {
		registry.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the notification refresh")
	}
	if !discoverer.refreshReturned.Load() {
		t.Fatal("Close returned before the notification refresh finished")
	}
}
