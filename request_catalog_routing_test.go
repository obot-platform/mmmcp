package mmmcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/testserver"
	"github.com/obot-platform/mmmcp/toolsearch"
)

// Promoted from the review reproducer: the longer prefix does not own this tool.
func TestOverlappingPrefixesRouteExactTool(t *testing.T) {
	for _, toolSearch := range []bool{false, true} {
		for _, warm := range []bool{false, true} {
			t.Run(fmt.Sprintf("search=%v/warm=%v", toolSearch, warm), func(t *testing.T) {
				handler := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{}, nil
				}
				definition := &mcp.Tool{
					Name:        "b__run",
					InputSchema: map[string]any{"type": "object"},
				}
				first := testserver.New(t, testserver.Options{Tools: []testserver.Tool{{
					Definition: definition,
					Handler:    handler,
				}}})
				second := testserver.New(t, testserver.Options{Tools: []testserver.Tool{{
					Definition: &mcp.Tool{
						Name:        "other",
						InputSchema: map[string]any{"type": "object"},
					},
					Handler: handler,
				}}})
				target, err := url.Parse(second.URL)
				if err != nil {
					t.Fatal(err)
				}

				proxy := httputil.NewSingleHostReverseProxy(target)
				var locked atomic.Bool
				var lockedRequests atomic.Int32
				secondProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if locked.Load() {
						lockedRequests.Add(1)
						w.Header().Set("WWW-Authenticate", `Bearer scope="unrelated"`)
						w.WriteHeader(http.StatusUnauthorized)

						return
					}

					proxy.ServeHTTP(w, r)
				}))
				defer secondProxy.Close()

				composite, err := mmmcp.New(t.Context(), &config.Config{}, mmmcp.Options{})
				if err != nil {
					t.Fatal(err)
				}

				defer composite.Close()
				cfg := &config.Config{
					RequestScopedDiscovery: true,
					ToolSearch:             toolSearch,
					Servers: []config.Server{
						{
							Name:   "first",
							Prefix: "a",
							URL:    first.URL,
						},
						{
							Name:   "second",
							Prefix: "a__b",
							URL:    secondProxy.URL,
						},
						{
							Name:   "third",
							Prefix: "c",
							URL:    first.URL,
						},
					},
				}
				request := func(method string, params map[string]any) json.RawMessage {
					t.Helper()
					params["_meta"] = map[string]any{
						"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
						"io.modelcontextprotocol/clientCapabilities": map[string]any{},
					}
					body, err := json.Marshal(map[string]any{
						"jsonrpc": "2.0",
						"id":      1,
						"method":  method,
						"params":  params,
					})
					if err != nil {
						t.Fatal(err)
					}

					req := httptest.NewRequest(http.MethodPost, "http://example.test/mcp", bytes.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Accept", "application/json, text/event-stream")
					req.Header.Set("Mcp-Method", method)
					req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
					if name, _ := params["name"].(string); name != "" {
						req.Header.Set("Mcp-Name", name)
					}

					recorder := httptest.NewRecorder()
					composite.HTTPHandler().ServeHTTP(recorder, req.WithContext(mmmcp.ContextWithConfig(req.Context(), cfg)))
					var response struct {
						Error  json.RawMessage `json:"error"`
						Result json.RawMessage `json:"result"`
					}
					if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil || len(response.Error) > 0 {
						t.Fatalf("%s failed: %d %s", method, recorder.Code, recorder.Body.String())
					}

					return response.Result
				}
				if warm {
					request("tools/list", map[string]any{})

					// Exact cached ownership must keep an overlapping locked component out of the call.
					locked.Store(true)
				}

				name := "a__b__run"
				arguments := map[string]any{}
				if toolSearch {
					exposed := *definition
					exposed.Name = name
					arguments = map[string]any{
						"name":     name,
						"revision": toolsearch.Revision(toolsearch.Reference{Name: name}, "first", definition.Name, &exposed),
					}
					name = toolsearch.CallToolName
				}

				result := request("tools/call", map[string]any{
					"name":      name,
					"arguments": arguments,
				})
				var called mcp.CallToolResult
				if json.Unmarshal(result, &called) != nil || called.IsError || len(first.Calls()) != 1 || len(second.Calls()) != 0 {
					t.Fatalf("wrong ownership: first calls=%d second calls=%d result=%s", len(first.Calls()), len(second.Calls()), result)
				}

				if warm && lockedRequests.Load() != 0 {
					t.Fatal("cached ownership rediscovered the overlapping locked component")
				}

				// Policy inspection and HTTP execution must use the same ownership selection.
				encoded, err := json.Marshal(arguments)
				if err != nil {
					t.Fatal(err)
				}

				resolved, ok, err := composite.ResolveToolCall(t.Context(), cfg, name, encoded)
				if err != nil || !ok || resolved.Route == nil || resolved.Route.Component.Name != "first" {
					t.Fatalf("policy resolution differs from execution: %+v, %v", resolved, err)
				}
			})
		}
	}
}
