package mmmcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/storage"
	"github.com/obot-platform/mmmcp/testserver"
)

func TestHTTPActivityRemovesClosedSessions(t *testing.T) {
	t.Parallel()

	fixture := testserver.New(t, testserver.Options{
		Stateful: true,
		Tools: []testserver.Tool{{
			Definition: &mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}},
			Handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
			},
		}},
	})
	composite, err := New(t.Context(), &config.Config{Servers: []config.Server{{Name: "fixture", URL: fixture.URL}}}, Options{
		Storage: storage.Options{DataDirectory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = composite.Close() })

	var deletes atomic.Int32
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
		}

		composite.HTTPHandler().ServeHTTP(w, r)
	}))
	t.Cleanup(frontend.Close)

	for i := range 3 {
		client := connectHTTPActivityClient(t, frontend)
		for range 3 {
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo"})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != "ok" {
				t.Fatalf("tool result = %+v, want ok", result)
			}
		}

		composite.activity.mu.Lock()
		session := composite.activity.sessions[client.ID()]
		retained := len(composite.activity.sessions)
		composite.activity.mu.Unlock()
		if session == nil || retained != 1 {
			t.Fatalf("session = %p, retained = %d, want one live session", session, retained)
		}

		waitHTTPActivity(t, "request activity to finish", func() bool {
			composite.activity.mu.Lock()
			defer composite.activity.mu.Unlock()

			return len(composite.activity.active) == 0
		})
		for range 10 {
			if got := composite.activity.bind(session); got != 0 {
				t.Fatalf("idle bind = %d, want 0", got)
			}
		}
		if got := httpActivityWatchers(&composite.activity); got != 1 {
			t.Fatalf("cleanup watchers after repeated calls and binds = %d, want 1", got)
		}

		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
		if got := deletes.Load(); got != int32(i+1) {
			t.Fatalf("frontend DELETE requests = %d, want %d", got, i+1)
		}
		waitHTTPActivity(t, "closed session removal", func() bool {
			composite.activity.mu.Lock()
			defer composite.activity.mu.Unlock()

			return len(composite.activity.sessions) == 0 && len(composite.activity.active) == 0
		})
		waitHTTPActivity(t, "cleanup to finish", func() bool { return httpActivityWatchers(&composite.activity) == 0 })
	}
}

func TestHTTPActivityReplacementSurvivesOldSessionClose(t *testing.T) {
	t.Parallel()

	firstClient, first := newHTTPActivitySession(t, "shared-id")
	secondClient, second := newHTTPActivitySession(t, "shared-id")
	if first == second || first.ID() != second.ID() {
		t.Fatal("expected distinct session pointers with the same ID")
	}

	var registry httpActivityRegistry
	registry.bind(first)
	registry.bind(second)
	if got := httpActivityWatchers(&registry); got != 2 {
		t.Fatalf("cleanup watchers = %d, want 2", got)
	}

	if err := firstClient.Close(); err != nil {
		t.Fatal(err)
	}
	// Wait for the old cleanup to finish, so a delayed deletion cannot hide a broken pointer check.
	waitHTTPActivity(t, "old session cleanup to finish", func() bool { return httpActivityWatchers(&registry) == 1 })
	if got := registry.session("shared-id"); got != second {
		t.Fatalf("replacement after old session close = %p, want %p", got, second)
	}

	if err := secondClient.Close(); err != nil {
		t.Fatal(err)
	}
	waitHTTPActivity(t, "replacement removal", func() bool { return registry.session("shared-id") == nil })
	waitHTTPActivity(t, "replacement cleanup to finish", func() bool { return httpActivityWatchers(&registry) == 0 })
}

func TestHTTPActivityBindPreservesCounts(t *testing.T) {
	t.Parallel()

	var registry httpActivityRegistry
	for _, session := range []*mcp.ServerSession{nil, {}} {
		if got := registry.bind(session); got != 0 {
			t.Fatalf("invalid session bind = %d, want 0", got)
		}
	}
	if registry.sessions != nil || registry.active != nil {
		t.Fatal("invalid binds initialized registry maps")
	}
	if registry.begin("") != 0 || registry.end("") != 0 {
		t.Fatal("empty ID changed activity")
	}

	client, session := newHTTPActivitySession(t, "counted-id")
	if got := registry.bind(session); got != 0 {
		t.Fatalf("initial bind = %d, want 0", got)
	}
	if registry.begin(session.ID()) != 1 || registry.begin(session.ID()) != 2 {
		t.Fatal("begin did not increment activity")
	}
	if got := registry.bind(session); got != 2 {
		t.Fatalf("active bind = %d, want 2", got)
	}
	if got := registry.end(session.ID()); got != 1 {
		t.Fatalf("end = %d, want 1", got)
	}
	if got := registry.bind(session); got != 1 {
		t.Fatalf("active bind = %d, want 1", got)
	}

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	waitHTTPActivity(t, "closed session removal during activity", func() bool { return registry.session(session.ID()) == nil })
	registry.mu.Lock()
	active := registry.active[session.ID()]
	registry.mu.Unlock()
	if active != 1 {
		t.Fatalf("activity after session removal = %d, want 1", active)
	}
	if registry.end(session.ID()) != 0 {
		t.Fatal("end did not clear activity")
	}
	if registry.end(session.ID()) != 0 {
		t.Fatal("end did not keep activity at zero")
	}
	waitHTTPActivity(t, "cleanup to finish", func() bool { return httpActivityWatchers(&registry) == 0 })
}

func connectHTTPActivityClient(t *testing.T, frontend *httptest.Server) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "activity-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             frontend.URL,
		HTTPClient:           frontend.Client(),
		DisableStandaloneSSE: true,
	}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return session
}

func newHTTPActivitySession(t *testing.T, id string) (*mcp.ClientSession, *mcp.ServerSession) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "activity-test", Version: "1"}, &mcp.ServerOptions{
		GetSessionID: func() string { return id },
	})
	frontend := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true}))
	t.Cleanup(frontend.Close)
	client := connectHTTPActivityClient(t, frontend)
	sessions := slices.Collect(server.Sessions())
	if len(sessions) != 1 {
		t.Fatalf("SDK sessions = %d, want 1", len(sessions))
	}
	t.Cleanup(func() { _ = sessions[0].Close() })

	return client, sessions[0]
}

func httpActivityWatchers(registry *httpActivityRegistry) int {
	registry.mu.Lock()
	defer registry.mu.Unlock()

	return registry.watchers
}

func waitHTTPActivity(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}

		time.Sleep(10 * time.Millisecond)
	}
}
