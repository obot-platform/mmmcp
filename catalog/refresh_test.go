package catalog_test

import (
	"context"
	"errors"
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
