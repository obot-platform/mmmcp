package mmmcp_test

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/config"
)

func TestDeferredAuthorizationKeepsSessionConnected(t *testing.T) {
	for _, protocol := range []string{"2025-11-25", "2026-07-28"} {
		for _, search := range []bool{false, true} {
			name := protocol
			if search {
				name += "/search"
			}
			t.Run(name, func(t *testing.T) {
				target, err := url.Parse(namedToolFixture(t, "fixture").URL)
				if err != nil {
					t.Fatal(err)
				}
				proxy := httputil.NewSingleHostReverseProxy(target)
				var authorized atomic.Bool
				upstream := testHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if !authorized.Load() {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					proxy.ServeHTTP(w, r)
				}))
				cfg := &config.Config{DeferAuthorization: true, ToolSearch: search, Servers: []config.Server{{Name: "fixture", URL: upstream.URL}}}
				composite, err := mmmcp.New(t.Context(), cfg, mmmcp.Options{
					AuthorizationURLs: func(context.Context) ([]mmmcp.AuthorizationURL, error) {
						return []mmmcp.AuthorizationURL{{Message: "Connect fixture", URL: "https://example.com/authorize?state=a%2Bb"}}, nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer composite.Close()
				frontend := testHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					composite.HTTPHandler().ServeHTTP(w, r.WithContext(mmmcp.ContextWithConfigID(r.Context(), "user")))
				}))
				changed, prompts, resources := make(chan struct{}, 10), make(chan struct{}, 10), make(chan struct{}, 10)
				client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, &mcp.ClientOptions{
					ToolListChangedHandler:     func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
					PromptListChangedHandler:   func(context.Context, *mcp.PromptListChangedRequest) { prompts <- struct{}{} },
					ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) { resources <- struct{}{} },
				})
				session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: frontend.URL, HTTPClient: frontend.Client()}, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close()
				assertOnlyTool(t, session, catalog.AuthorizationToolName)

				status := func() string {
					t.Helper()
					result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: catalog.AuthorizationToolName})
					if err != nil || result.IsError {
						t.Fatalf("status: %v %+v", err, result)
					}
					return result.Content[0].(*mcp.TextContent).Text
				}
				if text := status(); !strings.Contains(text, "fixture") || !strings.Contains(text, "Connect fixture: <https://example.com/authorize?state=a%2Bb>") {
					t.Fatalf("pending status: %q", text)
				}

				authorized.Store(true)
				if text := status(); !strings.Contains(text, "Authorization complete") {
					t.Fatalf("completed status: %q", text)
				}
				// Pending catalogs hide prompts and resources too.
				for family, notified := range map[string]chan struct{}{"tool": changed, "prompt": prompts, "resource": resources} {
					select {
					case <-notified:
					case <-time.After(5 * time.Second):
						t.Fatalf("missing %s list notification", family)
					}
				}
				listed, err := session.ListTools(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				for _, tool := range listed.Tools {
					if tool.Name == catalog.AuthorizationToolName {
						t.Fatal("authorization tool remained after sign-in")
					}
				}
				if !search {
					assertToolText(t, session, "tool", "fixture")
				}
			})
		}
	}
}

func TestPendingAuthorizationDiscoversOncePerRequest(t *testing.T) {
	for _, protocol := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(protocol, func(t *testing.T) {
			var discoveries atomic.Int32
			upstream := testHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				discoveries.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
			}))
			composite, err := mmmcp.New(t.Context(), &config.Config{DeferAuthorization: true, Servers: []config.Server{{Name: "oauth", URL: upstream.URL}}}, mmmcp.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer composite.Close()
			// New discovers once; one discovery can make several upstream requests.
			perDiscovery := discoveries.Load()
			frontend := testHTTPServer(t, composite.HTTPHandler())
			session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: frontend.URL, HTTPClient: frontend.Client()}, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			// notifications/initialized is handled after its response; let that discovery finish.
			assertOnlyTool(t, session, catalog.AuthorizationToolName)
			time.Sleep(100 * time.Millisecond)
			before := discoveries.Load()
			assertOnlyTool(t, session, catalog.AuthorizationToolName)
			if got := discoveries.Load() - before; got != perDiscovery {
				t.Fatalf("tools/list made %d upstream requests, want %d", got, perDiscovery)
			}
		})
	}
}
