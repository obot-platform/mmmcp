package catalog_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

type mutableDiscoverer struct {
	mu    sync.Mutex
	name  string
	err   error
	count int
}

type blockedRefreshDiscoverer struct {
	mu               sync.Mutex
	calls            int
	started          chan struct{}
	release          chan struct{}
	discoveryContext chan context.Context
	err              error
}

type refreshCredentialKey struct{}

type refreshDiscovererFunc func(context.Context, config.Server) (*component.Features, error)

func (f refreshDiscovererFunc) Discover(ctx context.Context, server config.Server) (*component.Features, error) {
	return f(ctx, server)
}

func TestSearchCatalogWaitsForRefresh(t *testing.T) {
	discoverer := &blockedRefreshDiscoverer{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	registry := catalog.NewRegistry(discoverer)
	defer registry.Close()
	cfg := &config.Config{
		ToolSearch: true,
		Servers:    []config.Server{{Name: "fixture", URL: "https://example.invalid"}},
	}
	_, fingerprint, err := registry.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	registry.RequestRefresh(fingerprint, nil)
	type result struct {
		catalog *catalog.Catalog
		err     error
	}
	done := make(chan result, 1)
	go func() {
		compiled, _, err := registry.Get(t.Context(), cfg)
		done <- result{catalog: compiled, err: err}
	}()

	select {
	case <-discoverer.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	select {
	case got := <-done:
		t.Fatalf("Get returned before refresh completed: %v", got.err)
	default:
	}

	close(discoverer.release)
	select {
	case got := <-done:
		if got.err != nil || got.catalog.Tools()[0].Name != "second" {
			t.Fatalf("Get returned %+v, %v", got.catalog, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Get did not resume after refresh")
	}
}

func TestSearchCatalogWaitsForExplicitRefresh(t *testing.T) {
	discoverer := &blockedRefreshDiscoverer{
		started: make(chan struct{}),
		release: make(chan struct{}),
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
	cfg := &config.Config{
		ToolSearch: true,
		Servers:    []config.Server{{Name: "fixture", URL: "https://example.invalid"}},
	}
	_, fingerprint, err := registry.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	refreshDone := make(chan error, 1)
	go func() {
		_, _, err := registry.Refresh(t.Context(), cfg)
		refreshDone <- err
	}()
	select {
	case <-discoverer.started:
	case <-time.After(time.Second):
		t.Fatal("explicit refresh did not start")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := registry.Get(ctx, cfg); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Get during explicit refresh returned %v, want deadline exceeded", err)
	}
	notified := make(chan bool, 1)
	registry.RequestRefresh(fingerprint, func(success bool) { notified <- success })
	close(discoverer.release)
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("explicit refresh failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("explicit refresh did not finish")
	}
	select {
	case success := <-notified:
		if !success {
			t.Fatal("notification refresh failed")
		}
	case <-time.After(time.Second):
		t.Fatal("notification during explicit refresh was lost")
	}
	current, _, err := registry.Get(t.Context(), cfg)
	if err != nil || current.Tools()[0].Name != "second" {
		t.Fatalf("Get after explicit refresh returned %+v, %v", current, err)
	}
	discoverer.mu.Lock()
	calls := discoverer.calls
	discoverer.mu.Unlock()
	if calls != 3 {
		t.Fatalf("discoveries = %d, want explicit refresh and notification refresh", calls)
	}
}

func TestSearchCatalogFailsClosedAfterFailedExplicitRefresh(t *testing.T) {
	failure := errors.New("discovery failed")
	discoverer := &blockedRefreshDiscoverer{
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
	cfg := &config.Config{
		ToolSearch: true,
		Servers:    []config.Server{{Name: "fixture", URL: "https://example.invalid"}},
	}
	if _, _, err := registry.Get(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}

	refreshDone := make(chan error, 1)
	go func() {
		_, _, err := registry.Refresh(t.Context(), cfg)
		refreshDone <- err
	}()
	select {
	case <-discoverer.started:
	case <-time.After(time.Second):
		t.Fatal("explicit refresh did not start")
	}
	close(discoverer.release)
	if err := <-refreshDone; !errors.Is(err, failure) {
		t.Fatalf("explicit refresh returned %v, want %v", err, failure)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, _, err := registry.Get(ctx, cfg); !errors.Is(err, catalog.ErrCatalogUnavailable) {
		t.Fatalf("failed explicit refresh exposed stale catalog: %v", err)
	}
}

func TestSearchCatalogRefreshWaitHonorsCancellation(t *testing.T) {
	discoverer := &blockedRefreshDiscoverer{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	registry := catalog.NewRegistry(discoverer)
	defer registry.Close()
	cfg := &config.Config{
		ToolSearch: true,
		Servers:    []config.Server{{Name: "fixture", URL: "https://example.invalid"}},
	}
	_, fingerprint, err := registry.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	registry.RequestRefresh(fingerprint, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, _, err := registry.Get(ctx, cfg)
		done <- err
	}()

	select {
	case <-discoverer.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Get returned %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Get did not stop waiting after cancellation")
	}
	close(discoverer.release)
}

func TestRegistryRefreshDebouncesAndKeepsLastKnownGood(t *testing.T) {
	discoverer := &mutableDiscoverer{name: "first"}
	registry := catalog.NewRegistry(discoverer)
	defer registry.Close()
	cfg := &config.Config{Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}
	first, fingerprint, err := registry.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	discoverer.set("second", nil)
	done := make(chan bool, 3)
	for range 3 {
		registry.RequestRefresh(fingerprint, func(success bool) { done <- success })
	}
	for range 3 {
		if !<-done {
			t.Fatal("successful refresh reported failure")
		}
	}
	second, _, err := registry.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || second.Tools()[0].Name != "second" {
		t.Fatalf("refreshed tools = %+v", second.Tools())
	}
	if got := discoverer.countValue(); got != 2 {
		t.Fatalf("discoveries = %d, want 2 after debounced refresh", got)
	}

	discoverer.set("broken", errors.New("discovery failed"))
	failed := make(chan bool, 1)
	registry.RequestRefresh(fingerprint, func(success bool) { failed <- success })
	select {
	case success := <-failed:
		if success {
			t.Fatal("failed refresh reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("refresh callback timed out")
	}
	retained, _, err := registry.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if retained != second || retained.Tools()[0].Name != "second" {
		t.Fatalf("failed refresh replaced last-known-good catalog: %+v", retained.Tools())
	}
}

func TestSearchCatalogFailsClosedDuringAndAfterFailedRefresh(t *testing.T) {
	discoverer := &mutableDiscoverer{name: "first"}
	registry := catalog.NewRegistry(discoverer)
	defer registry.Close()

	cfg := &config.Config{
		ToolSearch: true,
		Servers: []config.Server{{
			Name: "fixture",
			URL:  "https://example.invalid",
		}},
	}
	_, fingerprint, err := registry.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	discoverer.set("broken", errors.New("discovery failed"))
	done := make(chan bool, 1)
	registry.RequestRefresh(fingerprint, func(success bool) {
		done <- success
	})
	if _, _, err := registry.Get(t.Context(), cfg); !errors.Is(err, catalog.ErrCatalogUnavailable) {
		t.Fatalf("pending refresh returned %v", err)
	}
	if <-done {
		t.Fatal("failed refresh reported success")
	}
	if _, _, err := registry.Get(t.Context(), cfg); !errors.Is(err, catalog.ErrCatalogUnavailable) {
		t.Fatalf("failed refresh exposed stale catalog: %v", err)
	}

	discoverer.set("second", nil)
	done = make(chan bool, 1)
	registry.RequestRefresh(fingerprint, func(success bool) {
		done <- success
	})
	if !<-done {
		t.Fatal("recovery refresh failed")
	}

	current, _, err := registry.Get(t.Context(), cfg)
	if err != nil || current.Tools()[0].Name != "second" {
		t.Fatalf("recovered catalog = %+v, err = %v", current, err)
	}
}

func (d *mutableDiscoverer) Discover(context.Context, config.Server) (*component.Features, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.count++
	if d.err != nil {
		return nil, d.err
	}
	return &component.Features{Tools: []*mcp.Tool{{Name: d.name, InputSchema: map[string]any{"type": "object"}}}}, nil
}

func (d *mutableDiscoverer) set(name string, err error) {
	d.mu.Lock()
	d.name, d.err = name, err
	d.mu.Unlock()
}

func (d *mutableDiscoverer) countValue() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.count
}

func (d *blockedRefreshDiscoverer) Discover(ctx context.Context, _ config.Server) (*component.Features, error) {
	d.mu.Lock()
	d.calls++
	call := d.calls
	d.mu.Unlock()
	if call == 2 {
		if d.discoveryContext != nil {
			d.discoveryContext <- ctx
		}
		close(d.started)
		select {
		case <-d.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if call > 1 && d.err != nil {
		return nil, d.err
	}
	name := "first"
	if call > 1 {
		name = "second"
	}
	return &component.Features{Tools: []*mcp.Tool{{Name: name, InputSchema: map[string]any{"type": "object"}}}}, nil
}

func TestSearchRefreshOutlivesCaller(t *testing.T) {
	for _, miss := range []bool{false, true} {
		for _, deadline := range []bool{false, true} {
			name := "explicit"
			if miss {
				name = "miss"
			}
			if deadline {
				name += "/deadline"
			} else {
				name += "/cancel"
			}
			t.Run(name, func(t *testing.T) {
				d := &blockedRefreshDiscoverer{started: make(chan struct{}), release: make(chan struct{}), discoveryContext: make(chan context.Context, 1)}
				r := catalog.NewRegistry(d)
				defer r.Close()
				var release sync.Once
				defer release.Do(func() { close(d.release) })
				cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}
				if _, _, err := r.Get(t.Context(), cfg); err != nil {
					t.Fatal(err)
				}
				caller := context.WithValue(t.Context(), refreshCredentialKey{}, "credential")
				ctx, cancel := context.WithCancel(caller)
				wantErr := context.Canceled
				if deadline {
					cancel()
					ctx, cancel = context.WithTimeout(caller, 100*time.Millisecond)
					wantErr = context.DeadlineExceeded
				}
				defer cancel()
				done := make(chan error, 1)
				go func() {
					if miss {
						_, err := r.RefreshOnMiss(ctx, cfg)
						done <- err
					} else {
						_, _, err := r.Refresh(ctx, cfg)
						done <- err
					}
				}()
				select {
				case <-d.started:
				case <-time.After(time.Second):
					t.Fatal("refresh did not start")
				}
				discoveryCtx := <-d.discoveryContext
				waiter := make(chan error, 1)
				if miss {
					// A miss does not mark the catalog stale, so readers keep the snapshot.
					compiled, _, err := r.Get(t.Context(), cfg)
					if err != nil || compiled.Tools()[0].Name != "first" {
						t.Fatalf("reader during miss refresh = %v, %v; want current snapshot", compiled, err)
					}
				} else {
					observed := &observedDoneContext{Context: t.Context(), observed: make(chan struct{})}
					go func() {
						compiled, _, err := r.Get(observed, cfg)
						if err == nil && compiled.Tools()[0].Name != "second" {
							err = errors.New("waiter received old catalog")
						}
						waiter <- err
					}()
					select {
					case <-observed.observed:
					case <-time.After(time.Second):
						t.Fatal("catalog waiter did not start")
					}
				}
				if !deadline {
					cancel()
				}
				select {
				case err := <-done:
					if !errors.Is(err, wantErr) {
						t.Fatalf("refresh error = %v, want %v", err, wantErr)
					}
				case <-time.After(time.Second):
					t.Fatal("caller did not stop waiting")
				}
				if err := discoveryCtx.Err(); err != nil {
					t.Fatalf("shared discovery canceled: %v", err)
				}
				if discoveryCtx.Value(refreshCredentialKey{}) != "credential" {
					t.Fatal("shared discovery lost caller credentials")
				}
				select {
				case err := <-waiter:
					t.Fatalf("waiter returned before discovery finished: %v", err)
				default:
				}
				release.Do(func() { close(d.release) })
				if miss {
					// A later miss reuses the detached discovery's result.
					compiled, err := r.RefreshOnMiss(t.Context(), cfg)
					if err != nil || compiled.Tools()[0].Name != "second" {
						t.Fatalf("subsequent miss = %v, %v; want refreshed catalog", compiled, err)
					}
				} else {
					select {
					case err := <-waiter:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(time.Second):
						t.Fatal("catalog waiter did not resume")
					}
				}
				if _, _, err := r.Get(t.Context(), cfg); err != nil {
					t.Fatalf("healthy next caller: %v", err)
				}
				calls := func() int {
					d.mu.Lock()
					defer d.mu.Unlock()
					return d.calls
				}()
				if calls != 2 {
					t.Fatalf("discoveries = %d, want 2", calls)
				}
			})
		}
	}
}

func TestSearchRefreshRejectsCanceledCaller(t *testing.T) {
	for _, miss := range []bool{false, true} {
		name := "explicit"
		if miss {
			name = "miss"
		}
		t.Run(name, func(t *testing.T) {
			d := &mutableDiscoverer{name: "fixture"}
			r := catalog.NewRegistry(d)
			defer r.Close()
			cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}
			if _, _, err := r.Get(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			var err error
			if miss {
				_, err = r.RefreshOnMiss(ctx, cfg)
			} else {
				_, _, err = r.Refresh(ctx, cfg)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want canceled", err)
			}
			if d.countValue() != 1 {
				t.Fatal("canceled caller started discovery")
			}
			if _, err := r.RefreshOnMiss(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}
			if d.countValue() != 2 {
				t.Fatal("canceled caller consumed miss-refresh interval")
			}
		})
	}
}

func TestSearchDetachedRefreshStopsOnClose(t *testing.T) {
	d := &blockedRefreshDiscoverer{started: make(chan struct{}), release: make(chan struct{}), discoveryContext: make(chan context.Context, 1)}
	r := catalog.NewRegistry(d)
	defer r.Close()
	cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}
	if _, _, err := r.Get(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := r.Refresh(t.Context(), cfg); done <- err }()
	select {
	case <-d.started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	discoveryCtx := <-d.discoveryContext
	r.Close()
	select {
	case <-discoveryCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("discovery did not stop on close")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh caller did not resume")
	}
}

func TestMissRefreshFailureKeepsSnapshot(t *testing.T) {
	for _, toolSearch := range []bool{false, true} {
		for _, failure := range []error{errors.New("downstream unavailable"), context.DeadlineExceeded, context.Canceled} {
			name := "plain/"
			if toolSearch {
				name = "search/"
			}
			t.Run(name+failure.Error(), func(t *testing.T) {
				d := &mutableDiscoverer{name: "fixture"}
				r := catalog.NewRegistry(d)
				defer r.Close()
				cfg := &config.Config{ToolSearch: toolSearch, Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}
				if _, _, err := r.Get(t.Context(), cfg); err != nil {
					t.Fatal(err)
				}
				d.set("missing", failure)
				compiled, err := r.RefreshOnMiss(t.Context(), cfg)
				if err != nil || compiled == nil {
					t.Fatalf("failed miss refresh = %v, %v; want current snapshot", compiled, err)
				}
				for range 3 {
					if _, err := r.RefreshOnMiss(t.Context(), cfg); err != nil {
						t.Fatalf("repeated miss: %v", err)
					}
					compiled, _, err := r.Get(t.Context(), cfg)
					if err != nil {
						t.Fatalf("Get after failed miss refresh: %v", err)
					}
					if toolSearch && compiled.Tools()[0].Name != "fixture" {
						t.Fatalf("catalog tool = %q, want previous snapshot", compiled.Tools()[0].Name)
					}
				}
				if d.countValue() != 2 {
					t.Fatalf("discoveries = %d, want initial catalog and one bounded miss refresh", d.countValue())
				}
			})
		}
	}
}

func TestSearchMissRefreshDoesNotBlockReaders(t *testing.T) {
	d := &blockedRefreshDiscoverer{started: make(chan struct{}), release: make(chan struct{})}
	r := catalog.NewRegistry(d)
	defer r.Close()
	var release sync.Once
	defer release.Do(func() { close(d.release) })
	cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}
	if _, _, err := r.Get(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	type result struct {
		catalog *catalog.Catalog
		err     error
	}
	missed := make(chan result, 1)
	go func() {
		compiled, err := r.RefreshOnMiss(t.Context(), cfg)
		missed <- result{compiled, err}
	}()
	select {
	case <-d.started:
	case <-time.After(time.Second):
		t.Fatal("miss refresh did not start")
	}
	for range 3 {
		compiled, _, err := r.Get(t.Context(), cfg)
		if err != nil || compiled.Tools()[0].Name != "first" {
			t.Fatalf("reader during miss refresh = %v, %v; want current snapshot", compiled, err)
		}
	}
	select {
	case got := <-missed:
		t.Fatalf("miss caller returned before discovery finished: %v", got.err)
	default:
	}
	release.Do(func() { close(d.release) })
	select {
	case got := <-missed:
		if got.err != nil || got.catalog.Tools()[0].Name != "second" {
			t.Fatalf("miss caller = %v, %v; want refreshed catalog", got.catalog, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("miss caller did not resume")
	}
}

func TestSearchNotificationDuringMissRefreshBlocksReaders(t *testing.T) {
	d := &blockedRefreshDiscoverer{started: make(chan struct{}), release: make(chan struct{})}
	r := catalog.NewRegistry(d)
	defer r.Close()
	var release sync.Once
	defer release.Do(func() { close(d.release) })
	cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}
	_, fingerprint, err := r.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = r.RefreshOnMiss(t.Context(), cfg) }()
	select {
	case <-d.started:
	case <-time.After(time.Second):
		t.Fatal("miss refresh did not start")
	}
	notified := make(chan bool, 1)
	r.RequestRefresh(fingerprint, func(success bool) { notified <- success })

	observed := &observedDoneContext{Context: t.Context(), observed: make(chan struct{})}
	waiter := make(chan error, 1)
	go func() {
		compiled, _, err := r.Get(observed, cfg)
		if err == nil && compiled.Tools()[0].Name != "second" {
			err = errors.New("waiter received old catalog")
		}
		waiter <- err
	}()
	select {
	case <-observed.observed:
	case <-time.After(time.Second):
		t.Fatal("reader did not wait for notification refresh")
	}
	release.Do(func() { close(d.release) })
	if success := <-notified; !success {
		t.Fatal("notification refresh failed")
	}
	select {
	case err := <-waiter:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not resume")
	}
	d.mu.Lock()
	calls := d.calls
	d.mu.Unlock()
	if calls != 3 {
		t.Fatalf("discoveries = %d, want 3; notification did not rediscover after the miss", calls)
	}
}

func TestSearchMissCallerWaitsForNotificationRefresh(t *testing.T) {
	d := &blockedRefreshDiscoverer{started: make(chan struct{}), release: make(chan struct{})}
	r := catalog.NewRegistry(d)
	defer r.Close()
	var release sync.Once
	defer release.Do(func() { close(d.release) })
	cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}
	_, fingerprint, err := r.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	missed := make(chan error, 1)
	go func() {
		_, err := r.RefreshOnMiss(t.Context(), cfg)
		missed <- err
	}()
	select {
	case <-d.started:
	case <-time.After(time.Second):
		t.Fatal("miss refresh did not start")
	}
	r.RequestRefresh(fingerprint, nil)
	release.Do(func() { close(d.release) })

	select {
	case err := <-missed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("miss caller did not resume")
	}
	d.mu.Lock()
	calls := d.calls
	d.mu.Unlock()
	if calls != 3 {
		t.Fatalf("discoveries = %d when the miss caller resumed, want 3; it received the catalog the notification made stale", calls)
	}
}

func TestSearchRefreshKeepsCallerValues(t *testing.T) {
	for _, miss := range []bool{false, true} {
		name := "explicit"
		if miss {
			name = "miss"
		}
		t.Run(name, func(t *testing.T) {
			discoveries := make(chan context.Context, 2)
			d := refreshDiscovererFunc(func(ctx context.Context, _ config.Server) (*component.Features, error) {
				discoveries <- ctx
				return &component.Features{Tools: []*mcp.Tool{{Name: "fixture", InputSchema: map[string]any{"type": "object"}}}}, nil
			})
			r := catalog.NewRegistry(d)
			defer r.Close()
			cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}
			if _, _, err := r.Get(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}
			<-discoveries
			ctx := context.WithValue(t.Context(), refreshCredentialKey{}, "caller")
			ctx = component.ContextWithRequestHeaders(ctx, http.Header{"Authorization": {"Bearer caller"}})
			ctx = component.ContextWithClientInfo(ctx, &mcp.Implementation{Name: "frontend", Version: "1"})
			var err error
			if miss {
				_, err = r.RefreshOnMiss(ctx, cfg)
			} else {
				_, _, err = r.Refresh(ctx, cfg)
			}
			if err != nil {
				t.Fatal(err)
			}
			discoveryCtx := <-discoveries
			if got := discoveryCtx.Value(refreshCredentialKey{}); got != "caller" {
				t.Fatalf("discovery credential = %v, want caller", got)
			}
			if got := component.RequestHeadersFromContext(discoveryCtx).Get("Authorization"); got != "Bearer caller" {
				t.Fatalf("discovery Authorization = %q, want caller header", got)
			}
			if got := component.ClientInfoFromContext(discoveryCtx); got == nil || got.Name != "frontend" {
				t.Fatalf("discovery client info = %v, want frontend", got)
			}
		})
	}
}

func TestSearchMissRefreshWaiterKeepsInterval(t *testing.T) {
	d := &blockedRefreshDiscoverer{started: make(chan struct{}), release: make(chan struct{})}
	r := catalog.NewRegistry(d)
	defer r.Close()
	cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}
	_, fingerprint, err := r.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	notified := make(chan bool, 1)
	r.RequestRefresh(fingerprint, func(success bool) { notified <- success })
	select {
	case <-d.started:
	case <-time.After(time.Second):
		t.Fatal("notification refresh did not start")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waiting := &observedDoneContext{Context: ctx, observed: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := r.RefreshOnMiss(waiting, cfg)
		done <- err
	}()
	select {
	case <-waiting.observed:
	case <-time.After(time.Second):
		t.Fatal("miss caller did not wait for notification refresh")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("miss error = %v, want canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("miss caller did not stop waiting")
	}

	close(d.release)
	if success := <-notified; !success {
		t.Fatal("notification refresh failed")
	}
	if _, err := r.RefreshOnMiss(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	calls := d.calls
	d.mu.Unlock()
	if calls != 3 {
		t.Fatalf("discoveries = %d, want 3; waiting caller consumed miss-refresh interval", calls)
	}
}

func TestSearchMissRefreshReusesAwaitedRefresh(t *testing.T) {
	d := &blockedRefreshDiscoverer{started: make(chan struct{}), release: make(chan struct{})}
	r := catalog.NewRegistry(d)
	defer r.Close()
	cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}
	_, fingerprint, err := r.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	notified := make(chan bool, 1)
	r.RequestRefresh(fingerprint, func(success bool) { notified <- success })
	select {
	case <-d.started:
	case <-time.After(time.Second):
		t.Fatal("notification refresh did not start")
	}

	waiting := &observedDoneContext{Context: t.Context(), observed: make(chan struct{})}
	type result struct {
		catalog *catalog.Catalog
		err     error
	}
	done := make(chan result, 1)
	go func() {
		compiled, err := r.RefreshOnMiss(waiting, cfg)
		done <- result{compiled, err}
	}()
	select {
	case <-waiting.observed:
	case <-time.After(time.Second):
		t.Fatal("miss caller did not wait for notification refresh")
	}
	close(d.release)
	if success := <-notified; !success {
		t.Fatal("notification refresh failed")
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.catalog.Tools()[0].Name != "second" {
			t.Fatalf("miss catalog tool = %q, want refreshed catalog", got.catalog.Tools()[0].Name)
		}
	case <-time.After(time.Second):
		t.Fatal("miss caller did not resume")
	}
	d.mu.Lock()
	calls := d.calls
	d.mu.Unlock()
	if calls != 2 {
		t.Fatalf("discoveries = %d, want 2; miss started another discovery", calls)
	}
}

func TestSearchCatalogRefreshesOnNotificationAfterFailedMiss(t *testing.T) {
	d := &mutableDiscoverer{name: "first"}
	r := catalog.NewRegistry(d)
	defer r.Close()
	cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}
	_, fingerprint, err := r.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("downstream unavailable")
	d.set("first", failure)
	if _, err := r.RefreshOnMiss(t.Context(), cfg); err != nil {
		t.Fatalf("failed miss refresh: %v", err)
	}

	d.set("second", nil)
	r.RequestRefresh(fingerprint, nil)
	compiled, _, err := r.Get(t.Context(), cfg)
	if err != nil {
		t.Fatalf("Get during scheduled refresh: %v", err)
	}
	if compiled.Tools()[0].Name != "second" {
		t.Fatalf("catalog tool = %q, want refreshed catalog", compiled.Tools()[0].Name)
	}
}
