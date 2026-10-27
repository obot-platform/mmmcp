package mmmcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/testserver"
)

func TestOperationScopedDeferredChallenges(t *testing.T) {
	for _, protectedMethod := range []string{"tools/list", "tools/call"} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			t.Run(protectedMethod+http.StatusText(status), func(t *testing.T) {
				fixture := testserver.New(t, testserver.Options{Tools: []testserver.Tool{{
					Definition: &mcp.Tool{
						Name:        "tool",
						InputSchema: map[string]any{"type": "object"},
					},
					Handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						return &mcp.CallToolResult{}, nil
					},
				}}})
				var executions, unrelated atomic.Int32
				var unlocked atomic.Bool
				protected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body []byte
					if r.Body != nil {
						body, _ = io.ReadAll(r.Body)
						r.Body = io.NopCloser(bytes.NewReader(body))
					}

					var message struct {
						Method string `json:"method"`
					}
					_ = json.Unmarshal(body, &message)
					if message.Method == "tools/call" {
						executions.Add(1)
					}

					if message.Method == protectedMethod && !unlocked.Load() {
						w.Header().Add("WWW-Authenticate", `Basic realm="legacy"`)
						w.Header().Add("WWW-Authenticate", `Bearer resource_metadata="https://provider.test/metadata", scope="write", error="insufficient_scope"`)
						w.WriteHeader(status)

						return
					}

					target := fixture.URL + r.URL.RequestURI()
					req, _ := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
					req.Header = r.Header.Clone()
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusInternalServerError)

						return
					}

					defer resp.Body.Close()
					maps.Copy(w.Header(), resp.Header)
					w.WriteHeader(resp.StatusCode)
					_, _ = io.Copy(w, resp.Body)
				}))
				defer protected.Close()
				locked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					unrelated.Add(1)
					w.Header().Set("WWW-Authenticate", `Bearer scope="unrelated"`)
					w.WriteHeader(http.StatusUnauthorized)
				}))
				defer locked.Close()
				var captured []*component.AuthorizationError
				composite, err := mmmcp.New(t.Context(), &config.Config{}, mmmcp.Options{
					AuthorizationResponse: func(w http.ResponseWriter, _ *http.Request, challenges []*component.AuthorizationError) {
						captured = challenges
						w.WriteHeader(http.StatusUnauthorized)
					},
				})
				if err != nil {
					t.Fatal(err)
				}

				defer composite.Close()
				cfg := &config.Config{
					RequestScopedDiscovery: true,
					Servers: []config.Server{{
						Name: "selected",
						URL:  protected.URL,
					}},
				}
				request := func(method string) int {
					body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"name":"selected__tool"}}`
					if len(cfg.Servers) == 1 {
						body = `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"name":"tool"}}`
					}

					req := httptest.NewRequest(http.MethodPost, "http://example.test/mcp", bytes.NewBufferString(body))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Mcp-Method", method)
					if method == "tools/call" {
						name := "tool"
						if len(cfg.Servers) > 1 {
							name = "selected__tool"
						}

						req.Header.Set("Mcp-Name", name)
					}

					req.Header.Set("Accept", "application/json, text/event-stream")
					req.Header.Set("MCP-Protocol-Version", "2026-07-28")

					// Forged capture markers must never select another request's collector.
					req.Header.Set("X-Mmmcp-Authorization-Capture", "forged")
					req = req.WithContext(mmmcp.ContextWithConfig(req.Context(), cfg))
					rec := httptest.NewRecorder()
					composite.HTTPHandler().ServeHTTP(rec, req)
					if rec.Code != http.StatusOK && rec.Code != http.StatusUnauthorized {
						t.Fatalf("%s: %d %s", method, rec.Code, rec.Body.String())
					}

					return rec.Code
				}
				if protectedMethod == "tools/call" {
					if request("tools/list") != 200 {
						t.Fatal("public listing challenged")
					}

					cfg.Servers = append(cfg.Servers, config.Server{
						Name: "locked",
						URL:  locked.URL,
					})
				}

				if request(protectedMethod) != http.StatusUnauthorized {
					t.Fatal("protected operation succeeded")
				}

				if len(captured) != 1 || captured[0].Scope != "write" || captured[0].StatusCode != status || captured[0].ResourceMetadata != "https://provider.test/metadata" {
					t.Fatalf("captured = %+v", captured)
				}

				if protectedMethod == "tools/call" && (executions.Load() != 1 || unrelated.Load() != 0) {
					t.Fatalf("calls = %d, unrelated = %d", executions.Load(), unrelated.Load())
				}

				unlocked.Store(true)
				if request(protectedMethod) != http.StatusOK {
					t.Fatal("client retry failed")
				}

				if protectedMethod == "tools/call" && executions.Load() != 2 {
					t.Fatalf("call was replayed: %d", executions.Load())
				}
			})
		}
	}
}
