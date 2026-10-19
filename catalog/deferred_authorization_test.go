package catalog

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/toolsearch"
)

type discoverFunc func(context.Context, config.Server) (*component.Features, error)

type authDiscoverer struct {
	calls      atomic.Int32
	authorized atomic.Bool
	tool       string
}

func (d *authDiscoverer) Discover(_ context.Context, server config.Server) (*component.Features, error) {
	d.calls.Add(1)
	if server.Name == "oauth" && !d.authorized.Load() {
		return nil, &component.AuthorizationError{StatusCode: http.StatusUnauthorized}
	}
	return &component.Features{Tools: []*mcp.Tool{{Name: d.tool, InputSchema: map[string]any{"type": "object"}}}}, nil
}

func (f discoverFunc) Discover(ctx context.Context, server config.Server) (*component.Features, error) {
	return f(ctx, server)
}

func TestPendingAuthorizationIsNotCached(t *testing.T) {
	d := &authDiscoverer{tool: "tool"}
	r := NewRegistry(d)
	defer r.Close()
	cfg := &config.Config{DeferAuthorization: true, Servers: []config.Server{{Name: "oauth"}, {Name: "ready"}}}
	for range 2 {
		pending, _, err := r.Get(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got := pending.PendingAuthorization(); len(got) != 1 || got[0] != "oauth" {
			t.Fatalf("pending = %v", got)
		}
		if tools := pending.Tools(); len(tools) != 1 || tools[0].Name != AuthorizationToolName {
			t.Fatalf("pending catalog exposed %v", tools)
		}
		if _, ok := pending.RouteTool("ready__tool"); ok {
			t.Fatal("pending catalog routed a ready component")
		}
	}
	if d.calls.Load() != 4 {
		t.Fatalf("pending catalog was cached: %d discoveries", d.calls.Load())
	}

	d.authorized.Store(true)
	ready, _, err := r.Get(t.Context(), cfg)
	if err != nil || len(ready.PendingAuthorization()) != 0 || len(ready.Tools()) != 2 {
		t.Fatalf("after sign-in: %v %v", ready.Tools(), err)
	}
}

func TestRefreshDoesNotDeferAuthorization(t *testing.T) {
	d := &authDiscoverer{tool: "tool"}
	d.authorized.Store(true)
	r := NewRegistry(d)
	defer r.Close()
	cfg := &config.Config{DeferAuthorization: true, Servers: []config.Server{{Name: "oauth"}}}
	ready, fingerprint, err := r.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Background refreshes have no request credentials.
	d.authorized.Store(false)
	done := make(chan bool, 1)
	r.RequestRefresh(fingerprint, func(ok bool) { done <- ok })
	if <-done {
		t.Fatal("refresh without credentials succeeded")
	}
	if current, _, err := r.Get(t.Context(), cfg); err != nil || current != ready {
		t.Fatal("refresh replaced a ready catalog")
	}
}

func TestDeferredAuthorizationReservesToolName(t *testing.T) {
	d := &authDiscoverer{tool: AuthorizationToolName}
	d.authorized.Store(true)
	r := NewRegistry(d)
	defer r.Close()
	cfg := &config.Config{DeferAuthorization: true, Servers: []config.Server{{Name: "ready"}}}
	if _, _, err := r.Get(t.Context(), cfg); err == nil {
		t.Fatal("component tool shadowed the authorization tool")
	}
}

func TestPendingWaiterDiscoversWithOwnCredentials(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	r := NewRegistry(discoverFunc(func(ctx context.Context, _ config.Server) (*component.Features, error) {
		if component.RequestHeadersFromContext(ctx).Get("Authorization") == "signed-in" {
			return &component.Features{}, nil
		}
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil, &component.AuthorizationError{StatusCode: http.StatusUnauthorized}
	}))
	defer r.Close()
	cfg := &config.Config{DeferAuthorization: true, Servers: []config.Server{{Name: "oauth", PassthroughHeaders: []string{"Authorization"}}}}
	go func() {
		if _, _, err := r.Get(t.Context(), cfg); err != nil {
			t.Error(err)
		}
	}()
	<-started
	var fingerprint string
	done := make(chan *Catalog, 1)
	go func() {
		compiled, key, err := r.Get(component.ContextWithRequestHeaders(t.Context(), http.Header{"Authorization": {"signed-in"}}), cfg)
		if err != nil {
			t.Error(err)
		}
		fingerprint = key
		done <- compiled
	}()
	// Let the signed-in request wait on the other user's compile.
	time.Sleep(50 * time.Millisecond)
	close(release)
	compiled := <-done
	if compiled == nil || len(compiled.PendingAuthorization()) != 0 {
		t.Fatal("signed-in request received another user's pending catalog")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.entries[fingerprint]; entry == nil || entry.catalog != compiled {
		t.Fatal("ready catalog from the waiter was not cached")
	}
}

func TestPendingCatalogRejectsAuthorizationReference(t *testing.T) {
	pending, err := Compile(t.Context(), &config.Config{DeferAuthorization: true, Servers: []config.Server{{Name: "oauth"}}}, &authDiscoverer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := pending.RouteReference(toolsearch.Reference{Name: AuthorizationToolName}); ok {
		t.Fatal("authorization tool resolved to a downstream route")
	}
}

func TestConcurrentPendingWaitersDiscoverInParallel(t *testing.T) {
	const requests = 10
	started, release := make(chan struct{}), make(chan struct{})
	var calls, active, peak atomic.Int32
	r := NewRegistry(discoverFunc(func(context.Context, config.Server) (*component.Features, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		} else {
			now := active.Add(1)
			for {
				if old := peak.Load(); now <= old || peak.CompareAndSwap(old, now) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			active.Add(-1)
		}
		return nil, &component.AuthorizationError{StatusCode: http.StatusUnauthorized}
	}))
	defer r.Close()
	cfg := &config.Config{DeferAuthorization: true, Servers: []config.Server{{Name: "oauth"}}}
	var wg sync.WaitGroup
	wg.Go(func() {
		if _, _, err := r.Get(t.Context(), cfg); err != nil {
			t.Error(err)
		}
	})
	<-started
	for range requests - 1 {
		wg.Go(func() {
			if _, _, err := r.Get(t.Context(), cfg); err != nil {
				t.Error(err)
			}
		})
	}
	// Let the other requests wait on the first compile.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	// Re-entering the cache would serialize the waiters behind one another's compiles.
	if calls.Load() != requests || peak.Load() < 2 {
		t.Fatalf("%d requests: %d discoveries, peak concurrency %d", requests, calls.Load(), peak.Load())
	}
}
