package mmmcp

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/config"
)

// retryComponent is a stateful HTTP component that counts tool executions and
// can drop its sessions or the response to the next executed call.
type retryComponent struct {
	*httptest.Server
	calls atomic.Int32

	mu           sync.Mutex
	seen, reaped map[string]bool
	dropResponse bool
}

func newRetryComponent(t *testing.T) *retryComponent {
	t.Helper()
	c := &retryComponent{seen: map[string]bool{}, reaped: map[string]bool{}}
	server := mcp.NewServer(&mcp.Implementation{Name: "component", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c.calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("Mcp-Session-Id")
		c.mu.Lock()
		gone, drop := c.reaped[id], c.dropResponse
		if id != "" {
			c.seen[id] = true
		}
		c.mu.Unlock()
		if gone {
			// A JSON-RPC body on the 404, which the SDK would otherwise treat as a
			// per-call rejection that leaves the connection open.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32001,"message":"session not found"},"id":null}`))
			return
		}
		if !drop || r.Method != http.MethodPost {
			inner.ServeHTTP(w, r)
			return
		}
		// Execute the request, then cut the connection instead of answering if
		// it ran the tool.
		before := c.calls.Load()
		recorder := httptest.NewRecorder()
		inner.ServeHTTP(recorder, r)
		if c.calls.Load() == before {
			maps.Copy(w.Header(), recorder.Header())
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *retryComponent) reapSessions() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.seen {
		c.reaped[id] = true
	}
}

func newRetryFrontend(t *testing.T, url string) (*Composite, *mcp.ClientSession) {
	t.Helper()
	composite, err := New(t.Context(), &config.Config{Servers: []config.Server{{Name: "component", URL: url}}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = composite.Close() })
	frontend := httptest.NewServer(composite.HTTPHandler())
	t.Cleanup(frontend.Close)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "1"}, nil).Connect(t.Context(),
		&mcp.StreamableClientTransport{Endpoint: frontend.URL, HTTPClient: frontend.Client(), DisableStandaloneSSE: true},
		&mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return composite, session
}

// The first call after a component drops its session must be retried on a
// new session instead of failing back to the client, and the replacement
// runtime must keep the frontend's binding so its notifications still arrive.
func TestStatefulCallRetriesAfterComponentDropsSession(t *testing.T) {
	component := newRetryComponent(t)
	composite, session := newRetryFrontend(t, component.URL)

	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo"}); err != nil {
		t.Fatal(err)
	}
	bound := frontendBindingCounts(&composite.bindings)
	component.reapSessions()

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo"})
	if err != nil {
		t.Fatalf("first call after the session was dropped: %v", err)
	}
	if result.IsError {
		t.Fatalf("first call after the session was dropped returned a tool error: %+v", result.Content)
	}
	if got := component.calls.Load(); got != 2 {
		t.Fatalf("tool ran %d times, want 2 (no duplicate execution)", got)
	}
	// Notifications reach a frontend only while it keeps both its server
	// association and its component binding.
	if got := frontendBindingCounts(&composite.bindings); got != bound {
		t.Fatalf("bindings after replacement = %v, want %v", got, bound)
	}
}

// A call the component executed must never be replayed, even when the
// connection breaks before its response arrives.
func TestStatefulCallIsNotReplayedAfterExecution(t *testing.T) {
	component := newRetryComponent(t)
	_, session := newRetryFrontend(t, component.URL)

	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo"}); err != nil {
		t.Fatal(err)
	}
	component.mu.Lock()
	component.dropResponse = true
	component.mu.Unlock()

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo"})
	if err == nil && !result.IsError {
		t.Fatal("call whose response was lost succeeded")
	}
	if got := component.calls.Load(); got != 2 {
		t.Fatalf("tool ran %d times, want 2 (the lost call must not be replayed)", got)
	}
}
