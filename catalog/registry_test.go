package catalog_test

import (
	"context"
	"strings"
	"sync"
	"testing"

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

func (d *countingDiscoverer) Discover(context.Context, config.Server) (*component.Features, error) {
	d.mu.Lock()
	d.count++
	d.mu.Unlock()
	d.once.Do(func() { close(d.started) })
	<-d.release
	return &component.Features{Tools: []*mcp.Tool{{Name: "tool", InputSchema: map[string]any{"type": "object"}}}}, nil
}

func (d *countingDiscoverer) Count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.count
}
