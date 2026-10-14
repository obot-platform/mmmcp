package mmmcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/toolsearch"
)

func TestLegacyOutputSchemaProjection(t *testing.T) {
	upstream := schemaTranslationUpstream(t)
	composite, err := mmmcp.New(t.Context(), &config.Config{Servers: []config.Server{{
		Name: "fixture",
		URL:  upstream.URL,
	}}}, mmmcp.Options{DSN: "sqlite://:memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = composite.Close() })
	frontend := httptest.NewServer(composite.HTTPHandler())
	t.Cleanup(frontend.Close)
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			session := connectFrontend(t, frontend, &mcp.Implementation{Name: "projection", Version: "test"}, version)
			t.Cleanup(func() { _ = session.Close() })
			listed, err := session.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(listed.Tools) != 3 {
				t.Fatalf("retained %d tools, want 3", len(listed.Tools))
			}
			for _, tool := range listed.Tools {
				wantSchema := version == "2026-07-28" && tool.Name != "absent"
				if (tool.OutputSchema != nil) != wantSchema {
					t.Fatalf("%s outputSchema = %#v", tool.Name, tool.OutputSchema)
				}
			}
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "union"})
			if err != nil {
				t.Fatal(err)
			}
			assertToolOutput(t, result, map[string]any{"ok": true})
		})
	}
}

func schemaTranslationUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "schema-upstream", Version: "test"}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list":
				tools := []*mcp.Tool{
					{Name: "absent", InputSchema: map[string]any{"type": "object"}},
					{Name: "object", InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object"}},
					{Name: "union", InputSchema: map[string]any{"type": "object"}},
				}
				if req.(*mcp.ListToolsRequest).ProtocolVersion() >= "2026-07-28" {
					tools[2].OutputSchema = map[string]any{"oneOf": []any{map[string]any{"type": "null"}, map[string]any{"type": "object"}}}
				}
				return &mcp.ListToolsResult{Tools: tools}, nil
			case "tools/call":
				if req.(*mcp.CallToolRequest).Params.Name == "absent" {
					return &mcp.CallToolResult{Content: []mcp.Content{}, StructuredContent: []any{1, 2}}, nil
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}, StructuredContent: map[string]any{"ok": true}}, nil
			default:
				return next(ctx, method, req)
			}
		}
	})
	upstream := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(upstream.Close)
	return upstream
}

func TestGenericInvocationPreservesSearchSchemas(t *testing.T) {
	upstream := schemaTranslationUpstream(t)
	composite, err := mmmcp.New(t.Context(), &config.Config{
		ToolSearch: true,
		Servers:    []config.Server{{Name: "fixture", URL: upstream.URL}},
	}, mmmcp.Options{DSN: "sqlite://:memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = composite.Close() })
	frontend := httptest.NewServer(composite.HTTPHandler())
	t.Cleanup(frontend.Close)
	revisions := make(map[string]string)
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			session := connectFrontend(t, frontend, &mcp.Implementation{Name: "generic", Version: "test"}, version)
			t.Cleanup(func() { _ = session.Close() })
			for _, name := range []string{"union", "absent"} {
				search, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: toolsearch.SearchToolName, Arguments: map[string]any{"query": name}})
				if err != nil {
					t.Fatal(err)
				}
				found := decodeSearchResults(t, search)
				called := false
				for _, match := range found.Tools {
					if match.Reference.Name != name {
						continue
					}
					if (match.Tool.OutputSchema == nil) != (name == "absent") {
						t.Fatalf("%s search definition changed output schema: %#v", name, match.Tool.OutputSchema)
					}
					if previous, ok := revisions[name]; ok && previous != match.Revision {
						t.Fatal("frontend version changed the invocation revision")
					}
					revisions[name] = match.Revision
					called = true
					result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: toolsearch.CallToolName, Arguments: map[string]any{
						"name": name, "revision": match.Revision, "arguments": map[string]any{},
					}})
					if err != nil {
						t.Fatal(err)
					}
					var want any = map[string]any{"ok": true}
					if name == "absent" {
						want = []any{float64(1), float64(2)}
					}
					if version == "2025-11-25" && name == "absent" {
						if result.StructuredContent != nil || len(result.Content) != 1 {
							t.Fatalf("legacy array was not moved to text: %+v", result)
						}
						if text, ok := result.Content[0].(*mcp.TextContent); !ok || text.Text != "[1,2]" {
							t.Fatalf("legacy array data missing: %+v", result.Content)
						}
					} else {
						assertToolOutput(t, result, want)
					}
				}
				if !called {
					t.Fatalf("search did not retain %s", name)
				}
			}
		})
	}
}

// Output schema omission must not change structured tool results.
func assertToolOutput(t *testing.T, result *mcp.CallToolResult, want any) {
	t.Helper()
	if !reflect.DeepEqual(result.StructuredContent, want) {
		t.Fatalf("structured result = %#v, want %#v", result.StructuredContent, want)
	}
}

func protocolSchemaUpstream(t *testing.T, version string) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "protocol-fixture", Version: "test"}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "server/discover":
				if version < "2026-07-28" {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "legacy server"}
				}
			case "initialize":
				result, err := next(ctx, method, req)
				if err == nil {
					result.(*mcp.InitializeResult).ProtocolVersion = version
				}
				return result, err
			case "tools/list":
				if req.(*mcp.ListToolsRequest).ProtocolVersion() != version {
					return nil, fmt.Errorf("unexpected upstream listing protocol %s, want %s", req.(*mcp.ListToolsRequest).ProtocolVersion(), version)
				}
				return &mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "object", InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object"}}}}, nil
			case "tools/call":
				if req.(*mcp.CallToolRequest).ProtocolVersion() != version {
					return nil, fmt.Errorf("unexpected upstream call protocol %s, want %s", req.(*mcp.CallToolRequest).ProtocolVersion(), version)
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "authored"}}, StructuredContent: map[string]any{"ok": true}}, nil
			}
			return next(ctx, method, req)
		}
	})
	upstream := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	t.Cleanup(upstream.Close)
	return upstream
}

func decodeSearchResults(t *testing.T, result *mcp.CallToolResult) toolsearch.Results {
	t.Helper()
	if result.StructuredContent == nil || len(result.Content) != 1 {
		t.Fatalf("search result representations missing: %+v", result)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var structured, text toolsearch.Results
	if err := json.Unmarshal(data, &structured); err != nil {
		t.Fatal(err)
	}
	block, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("search content is not JSON text: %+v", result.Content)
	}
	if err := json.Unmarshal([]byte(block.Text), &text); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(structured, text) {
		t.Fatalf("search text and structured content disagree: %s vs %s", data, block.Text)
	}
	return structured
}
