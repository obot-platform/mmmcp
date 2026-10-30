package mmmcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
)

func TestScopedDiscoveryPreservesSessionsAndRefreshesFullCatalog(t *testing.T) {
	for _, protocol := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(protocol, func(t *testing.T) {
			testScopedDiscoveryState(t, protocol)
		})
	}
}

func testScopedDiscoveryState(t *testing.T, protocol string) {
	t.Helper()
	var state sync.Map
	upstream := mcp.NewServer(&mcp.Implementation{Name: "stateful", Version: "test"}, nil)
	tool := &mcp.Tool{Name: "create", InputSchema: map[string]any{"type": "object"}}
	upstream.AddTool(tool, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		state.Store(req.Session.ID(), true)

		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ResourceLink{URI: "file:///notes", Name: "notes"}}}, nil
	})
	upstream.AddResource(&mcp.Resource{URI: "file:///notes", Name: "notes"}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		text := "missing"
		if _, ok := state.Load(req.Session.ID()); ok {
			text = "created"
		}

		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: text}}}, nil
	})
	upstream.AddPrompt(&mcp.Prompt{Name: "notes"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.ResourceLink{URI: "file:///notes", Name: "notes"}}}}, nil
	})
	backend := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, &mcp.StreamableHTTPOptions{JSONResponse: true}))
	defer backend.Close()

	other := namedToolFixture(t, "other")
	var current atomic.Pointer[config.Config]
	current.Store(&config.Config{
		RequestScopedDiscovery: true,
		Servers: []config.Server{
			{Name: "stateful", Prefix: "a", URL: backend.URL},
			{Name: "other", Prefix: "b", URL: other.URL},
		},
	})
	composite, err := mmmcp.New(t.Context(), &config.Config{}, mmmcp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer composite.Close()

	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := mmmcp.ContextWithConfigID(r.Context(), "scoped")
		ctx = mmmcp.ContextWithConfig(ctx, current.Load())
		composite.HTTPHandler().ServeHTTP(w, r.WithContext(ctx))
	}))
	defer frontend.Close()

	changed := make(chan struct{}, 20)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
	})
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: frontend.URL}, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	listed, err := session.ListTools(t.Context(), nil)
	if err != nil || len(listed.Tools) != 2 {
		t.Fatalf("initial listing: %+v, %v", listed, err)
	}

	called, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "a__create"})
	if err != nil {
		t.Fatal(err)
	}
	uri := called.Content[0].(*mcp.ResourceLink).URI
	read, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri})
	if err != nil || protocol == "2025-11-25" && read.Contents[0].Text != "created" {
		t.Fatalf("resource read lost tool session state: %+v, %v", read, err)
	}

	prompt, err := session.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: "a__notes"})
	if err != nil || prompt.Messages[0].Content.(*mcp.ResourceLink).URI != uri {
		t.Fatalf("prompt resource identity differs: %+v, %v", prompt, err)
	}

	select {
	case <-changed:
		t.Fatal("partial catalogs emitted a false tools/list_changed notification")
	case <-time.After(150 * time.Millisecond):
	}

	if protocol == "2026-07-28" {
		// Stateless calls have no persistent upstream runtime to notify this frontend.
		return
	}

	// The notification originates from the owner-only tool runtime, but must also
	// invalidate the full tools/list catalog warmed above.
	upstream.AddTool(&mcp.Tool{Name: "added", InputSchema: map[string]any{"type": "object"}}, nil)
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream change was not forwarded")
	}

	listed, err = session.ListTools(t.Context(), nil)
	if err != nil || len(listed.Tools) != 3 {
		t.Fatalf("full listing stayed stale: %+v, %v", listed, err)
	}

	// A genuine credential change must still isolate the runtime.
	next := *current.Load()
	next.Servers = slices.Clone(next.Servers)
	next.Servers[0].Headers = map[string]string{"Authorization": "Bearer different-account"}
	current.Store(&next)
	read, err = session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri})
	if err != nil || read.Contents[0].Text != "missing" {
		t.Fatalf("new credentials reused the old runtime: %+v, %v", read, err)
	}
}
