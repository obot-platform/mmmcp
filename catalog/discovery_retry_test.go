package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

func pingFeatures() *component.Features {
	return &component.Features{Tools: []*mcp.Tool{{Name: "ping", InputSchema: map[string]any{"type": "object"}}}}
}

func urlServer(name string) config.Server {
	return config.Server{Name: name, URL: "https://example.invalid/" + name}
}

// A refresh callback may shut the registry down, for example after a failed
// refresh. Close waits for refreshes, so it must not wait for the one running it.
func TestRegistryCloseFromRefreshCallback(t *testing.T) {
	registry := NewRegistry(discoverFunc(func(context.Context, config.Server) (*component.Features, error) {
		return pingFeatures(), nil
	}))
	cfg := &config.Config{Servers: []config.Server{urlServer("a")}}
	_, fingerprint, err := registry.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	closed := make(chan struct{})
	registry.RequestRefresh(fingerprint, func(bool) {
		registry.Close()
		close(closed)
	})
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close called from a refresh callback deadlocked")
	}
}

func TestDiscoveryTimeoutPerComponent(t *testing.T) {
	var mu sync.Mutex
	remaining := map[string]time.Duration{}
	registry := NewRegistry(discoverFunc(func(ctx context.Context, server config.Server) (*component.Features, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return nil, fmt.Errorf("component %q discovered without a deadline", server.Name)
		}
		mu.Lock()
		remaining[server.Name] = time.Until(deadline)
		mu.Unlock()
		return pingFeatures(), nil
	}))
	defer registry.Close()

	unset, short, long := urlServer("unset"), urlServer("short"), urlServer("long")
	short.Timeout = 5 * time.Second
	long.Timeout = 5 * time.Minute
	if _, _, err := registry.Get(t.Context(), &config.Config{Servers: []config.Server{unset, short, long}}); err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]time.Duration{
		"unset": defaultDiscoveryTimeout,
		"short": 5 * time.Second,
		// A configured timeout is honored even when it exceeds the default.
		"long": 5 * time.Minute,
	} {
		if got := remaining[name]; got > want || got < want-time.Second {
			t.Errorf("%s: discovery deadline in %s, want about %s", name, got, want)
		}
	}
}

func TestDiscoveryTimeoutFailsHangingComponent(t *testing.T) {
	registry := NewRegistry(discoverFunc(func(ctx context.Context, server config.Server) (*component.Features, error) {
		if server.Name == "hangs" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return pingFeatures(), nil
	}))
	defer registry.Close()

	hangs := urlServer("hangs")
	hangs.Timeout = 50 * time.Millisecond
	start := time.Now()
	_, _, err := registry.Get(t.Context(), &config.Config{Servers: []config.Server{urlServer("a"), hangs}})
	if err == nil || !strings.Contains(err.Error(), `component "hangs": discovery timed out after 50ms`) {
		t.Fatalf("error = %v, want the hanging component's timeout", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("discovery took %s", elapsed)
	}
}

// Time spent queued behind the command limit must not count against a component.
func TestDiscoveryTimeoutExcludesQueueTime(t *testing.T) {
	registry := NewRegistry(discoverFunc(func(ctx context.Context, _ config.Server) (*component.Features, error) {
		select {
		case <-time.After(80 * time.Millisecond):
			return pingFeatures(), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}))
	defer registry.Close()

	cfg := &config.Config{}
	for i := range maxParallelCommandDiscoveries + 1 {
		cfg.Servers = append(cfg.Servers, config.Server{
			Name: fmt.Sprintf("cmd%02d", i), Command: "/bin/true", Timeout: 150 * time.Millisecond,
		})
	}
	if _, _, err := registry.Get(t.Context(), cfg); err != nil {
		t.Fatalf("queued component timed out: %v", err)
	}
}

// A client that retries straight after a failure, or falls back from
// server/discover to initialize, gets the same failure instead of a new discovery.
func TestRegistryReusesRecentFailure(t *testing.T) {
	var calls atomic.Int32
	var healthy atomic.Bool
	registry := NewRegistry(discoverFunc(func(_ context.Context, server config.Server) (*component.Features, error) {
		calls.Add(1)
		if server.Name == "broken" && !healthy.Load() {
			return nil, errors.New("connection refused")
		}
		return pingFeatures(), nil
	}))
	defer registry.Close()
	registry.failureTTL = 100 * time.Millisecond
	cfg := &config.Config{Servers: []config.Server{urlServer("a"), urlServer("broken")}}

	_, _, first := registry.Get(t.Context(), cfg)
	if first == nil {
		t.Fatal("discovery with a broken component succeeded")
	}
	discoveries := calls.Load()

	for range 3 {
		if _, _, err := registry.Get(t.Context(), cfg); err == nil || err.Error() != first.Error() {
			t.Fatalf("retry error = %v, want the cached %v", err, first)
		}
	}
	if got := calls.Load(); got != discoveries {
		t.Fatalf("retries rediscovered: %d discoveries, want %d", got, discoveries)
	}
	if _, ok := registry.Cached(cfg); ok {
		t.Fatal("a failure was reported as a cached catalog")
	}

	healthy.Store(true)
	time.Sleep(registry.failureTTL)
	if _, _, err := registry.Get(t.Context(), cfg); err != nil {
		t.Fatalf("discovery after the failure expired: %v", err)
	}
	if got := calls.Load(); got != discoveries+2 {
		t.Fatalf("discoveries after expiry = %d, want %d", got, discoveries+2)
	}
}

// Authorization depends on the caller's credentials, so its failure is not shared.
func TestRegistryDoesNotReuseAuthorizationFailure(t *testing.T) {
	var calls atomic.Int32
	registry := NewRegistry(discoverFunc(func(context.Context, config.Server) (*component.Features, error) {
		calls.Add(1)
		return nil, &component.AuthorizationError{StatusCode: http.StatusUnauthorized}
	}))
	defer registry.Close()
	cfg := &config.Config{Servers: []config.Server{urlServer("oauth")}}

	for range 2 {
		if _, _, err := registry.Get(t.Context(), cfg); len(component.AuthorizationErrors(err)) == 0 {
			t.Fatalf("error = %v, want an authorization failure", err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("discoveries = %d, want each request to discover with its own credentials", got)
	}
}

// A component's list-changed notification invalidates its views; a cached
// failure must not outlive that.
func TestInvalidateComponentClearsCachedFailure(t *testing.T) {
	var calls atomic.Int32
	registry := NewRegistry(discoverFunc(func(context.Context, config.Server) (*component.Features, error) {
		calls.Add(1)
		return nil, errors.New("connection refused")
	}))
	defer registry.Close()
	cfg := &config.Config{Servers: []config.Server{urlServer("broken")}}

	_, _, _ = registry.Get(t.Context(), cfg)
	registry.InvalidateComponent("broken")
	_, _, _ = registry.Get(t.Context(), cfg)
	if got := calls.Load(); got != 2 {
		t.Fatalf("discoveries = %d, want a new discovery after invalidation", got)
	}
}
