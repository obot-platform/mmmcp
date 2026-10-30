package catalog_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

type interruptedDiscoverer struct {
	failFirst bool
	calls     atomic.Int32
	started   chan struct{}
	release   chan struct{}
}

func (d *interruptedDiscoverer) Discover(ctx context.Context, _ config.Server) (*component.Features, error) {
	if d.calls.Add(1) == 1 {
		close(d.started)
		select {
		case <-d.release:
		case <-ctx.Done():
		}

		if d.failFirst {
			return nil, errors.New("old discovery failed")
		}
	}

	return &component.Features{Tools: []*mcp.Tool{{
		Name:        "echo",
		Description: "Echo a message",
		InputSchema: map[string]any{"type": "object"},
	}}}, nil
}

func TestInvalidatedCompileCannotEvictReplacement(t *testing.T) {
	discoverer := &interruptedDiscoverer{
		failFirst: true,
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	registry := catalog.NewRegistry(discoverer)
	defer registry.Close()
	cfg := &config.Config{Servers: []config.Server{{Name: "fixture"}}}
	finished := make(chan error, 1)
	go func() {
		_, _, err := registry.Get(t.Context(), cfg)
		finished <- err
	}()
	<-discoverer.started
	registry.InvalidateComponent("fixture")

	_, _, err := registry.Get(t.Context(), cfg)
	close(discoverer.release)
	if err != nil {
		t.Fatal(err)
	}

	if err := <-finished; err == nil {
		t.Fatal("expected the original compile to fail")
	}

	if _, ok := registry.Cached(cfg); !ok {
		t.Fatal("the invalidated compile evicted its successful replacement")
	}
}

func TestInvalidatedSuccessfulCompileIsSearchable(t *testing.T) {
	discoverer := &interruptedDiscoverer{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	registry := catalog.NewRegistry(discoverer)
	defer registry.Close()
	cfg := &config.Config{
		ToolSearch: true,
		Servers:    []config.Server{{Name: "fixture"}},
	}
	type result struct {
		compiled *catalog.Catalog
		err      error
	}
	finished := make(chan result, 1)
	go func() {
		compiled, _, err := registry.Get(t.Context(), cfg)
		finished <- result{
			compiled: compiled,
			err:      err,
		}
	}()
	<-discoverer.started
	registry.InvalidateComponent("fixture")

	replacement, _, err := registry.Get(t.Context(), cfg)
	close(discoverer.release)
	if err != nil {
		t.Fatal(err)
	}

	original := <-finished
	if original.err != nil {
		t.Fatal(original.err)
	}
	if original.compiled == replacement {
		t.Fatal("expected distinct catalogs across invalidation")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := searchCatalog(ctx, original.compiled, "echo", 10, 0); err != nil {
		t.Fatalf("the invalidated successful compile cannot search: %v", err)
	}

	cached, ok := registry.Cached(cfg)
	if !ok || cached != replacement {
		t.Fatal("the invalidated compile replaced its successor")
	}
}
