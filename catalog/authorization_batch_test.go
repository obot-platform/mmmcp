package catalog_test

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

type challengeDiscoverer struct{ calls atomic.Int32 }

func (d *challengeDiscoverer) Discover(ctx context.Context, server config.Server) (*component.Features, error) {
	d.calls.Add(1)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Duration(len(server.Name)) * time.Millisecond):
	}

	if server.Name == "public" {
		return &component.Features{}, nil
	}

	status, code := http.StatusUnauthorized, ""
	if server.Name == "component-11" {
		status, code = http.StatusForbidden, "insufficient_scope"
	}

	return nil, &component.AuthorizationError{
		StatusCode: status,
		ErrorCode:  code,
		Scope:      server.Name,
	}
}

func TestDiscoveryCollectsEveryAuthorizationFailureBeyondWorkerLimit(t *testing.T) {
	d := &challengeDiscoverer{}
	registry := catalog.NewRegistry(d)
	defer registry.Close()
	cfg := &config.Config{Servers: []config.Server{{Name: "public"}}}

	for i := range 12 {
		cfg.Servers = append(cfg.Servers, config.Server{
			Name: fmt.Sprintf("component-%02d", i),
			URL:  fmt.Sprintf("https://example.test/%d", i),
		})
	}

	for range 2 {
		_, _, err := registry.Get(t.Context(), cfg)
		challenges := component.AuthorizationErrors(err)
		if len(challenges) != 12 {
			t.Fatalf("challenges = %d, error = %v", len(challenges), err)
		}

		for i, challenge := range challenges {
			if challenge.Component != cfg.Servers[i+1].Name || challenge.URL != cfg.Servers[i+1].URL {
				t.Fatalf("challenge %d = %+v", i, challenge)
			}
		}
	}

	if d.calls.Load() != 26 {
		t.Fatalf("discovery calls = %d; auth failures must not be cached", d.calls.Load())
	}
}
