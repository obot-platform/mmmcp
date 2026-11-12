package catalog

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

// countingAuthDiscoverer challenges the "oauth" component unless the request
// is signed in, and counts discoveries per component.
type countingAuthDiscoverer struct {
	mu    sync.Mutex
	calls map[string]int
}

func (d *countingAuthDiscoverer) Discover(ctx context.Context, server config.Server) (*component.Features, error) {
	d.mu.Lock()
	d.calls[server.Name]++
	d.mu.Unlock()
	if server.Name == "oauth" && component.RequestHeadersFromContext(ctx).Get("Authorization") != "signed-in" {
		return nil, &component.AuthorizationError{StatusCode: http.StatusUnauthorized}
	}
	return pingFeatures(), nil
}

func (d *countingAuthDiscoverer) count(name string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[name]
}

func signedIn(ctx context.Context) context.Context {
	return component.ContextWithRequestHeaders(ctx, http.Header{"Authorization": {"signed-in"}})
}

func keptAuthConfig(deferAuthorization bool) *config.Config {
	oauth := urlServer("oauth")
	oauth.PassthroughHeaders = []string{"Authorization"}
	return &config.Config{
		DeferAuthorization: deferAuthorization,
		Servers:            []config.Server{urlServer("a"), urlServer("b"), oauth},
	}
}

// A request that is not signed in rediscovers only the component that
// challenged, not every component, until the catalog is ready.
func TestRegistryRediscoversOnlyChallengingComponents(t *testing.T) {
	for _, deferAuthorization := range []bool{false, true} {
		name := "challenge"
		if deferAuthorization {
			name = "pending"
		}
		t.Run(name, func(t *testing.T) {
			discoverer := &countingAuthDiscoverer{calls: map[string]int{}}
			registry := NewRegistry(discoverer)
			defer registry.Close()
			cfg := keptAuthConfig(deferAuthorization)

			for range 3 {
				compiled, _, err := registry.Get(t.Context(), cfg)
				if !needsAuthorization(compiled, err) {
					t.Fatalf("catalog = %v, error = %v, want authorization required", compiled, err)
				}
			}
			compiled, _, err := registry.Get(signedIn(t.Context()), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if len(compiled.PendingAuthorization()) != 0 || len(compiled.Tools()) != 3 {
				t.Fatalf("signed-in catalog has %d tools, pending %v", len(compiled.Tools()), compiled.PendingAuthorization())
			}

			for component, want := range map[string]int{"a": 1, "b": 1, "oauth": 4} {
				if got := discoverer.count(component); got != want {
					t.Errorf("%s discovered %d times, want %d", component, got, want)
				}
			}
			registry.mu.Lock()
			defer registry.mu.Unlock()
			if len(registry.kept) != 0 {
				t.Fatal("kept components outlived the ready catalog")
			}
		})
	}
}

// A component's list-changed notification invalidates its kept features too.
func TestInvalidateComponentDropsKeptFeatures(t *testing.T) {
	discoverer := &countingAuthDiscoverer{calls: map[string]int{}}
	registry := NewRegistry(discoverer)
	defer registry.Close()
	cfg := keptAuthConfig(false)

	_, _, _ = registry.Get(t.Context(), cfg)
	registry.InvalidateComponent("a")
	_, _, _ = registry.Get(t.Context(), cfg)
	if got := discoverer.count("a"); got != 2 {
		t.Fatalf("a discovered %d times, want a new discovery after invalidation", got)
	}
}

// Programmatic configurations may give distinct components the same name; each
// keeps its own features.
func TestRegistryKeepsSameNamedComponentsApart(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	registry := NewRegistry(discoverFunc(func(ctx context.Context, server config.Server) (*component.Features, error) {
		mu.Lock()
		calls[server.URL]++
		mu.Unlock()
		if server.Prefix == "oauth" && component.RequestHeadersFromContext(ctx).Get("Authorization") != "signed-in" {
			return nil, &component.AuthorizationError{StatusCode: http.StatusUnauthorized}
		}
		return pingFeatures(), nil
	}))
	defer registry.Close()
	public := config.Server{Name: "shared", Prefix: "public", URL: "https://public.invalid"}
	oauth := config.Server{Name: "shared", Prefix: "oauth", URL: "https://oauth.invalid", PassthroughHeaders: []string{"Authorization"}}
	cfg := &config.Config{Servers: []config.Server{public, oauth}}

	if _, _, err := registry.Get(t.Context(), cfg); len(component.AuthorizationErrors(err)) == 0 {
		t.Fatalf("error = %v, want an authorization failure", err)
	}
	compiled, _, err := registry.Get(signedIn(t.Context()), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if calls[oauth.URL] != 2 || calls[public.URL] != 1 {
		t.Fatalf("discoveries = %v, want the challenged component rediscovered and the other kept", calls)
	}
	for _, name := range []string{"public__ping", "oauth__ping"} {
		if _, ok := compiled.RouteTool(name); !ok {
			t.Errorf("catalog is missing %s", name)
		}
	}
}
