package catalog_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

type interruptedDiscoverer struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (d *interruptedDiscoverer) Discover(ctx context.Context, _ config.Server) (*component.Features, error) {
	if d.calls.Add(1) == 1 {
		close(d.started)
		select {
		case <-d.release:
		case <-ctx.Done():
		}

		return nil, errors.New("old discovery failed")
	}

	return &component.Features{}, nil
}

func TestInvalidatedCompileCannotEvictReplacement(t *testing.T) {
	discoverer := &interruptedDiscoverer{
		started: make(chan struct{}),
		release: make(chan struct{}),
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
