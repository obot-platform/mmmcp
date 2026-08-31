package mmmcp_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/testserver"
)

func TestSingleComponentUsesDownstreamServerIdentity(t *testing.T) {
	fixture := testserver.New(t, testserver.Options{})
	for _, test := range []struct {
		name        string
		server      config.Server
		wantName    string
		wantVersion string
	}{
		{name: "http", server: config.Server{Name: "fixture", URL: fixture.URL}, wantName: "mmmcp-test-component", wantVersion: "1.0.0"},
		{name: "stdio", server: stdioServerConfig(nil), wantName: "stdio-helper", wantVersion: "1.0.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			info := frontendServerInfo(t, &config.Config{Servers: []config.Server{test.server}})
			if info.Name != test.wantName || info.Version != test.wantVersion {
				t.Fatalf("server info = %+v, want %s/%s", info, test.wantName, test.wantVersion)
			}
		})
	}
}

func TestMultipleComponentsUseConfiguredOrDefaultServerIdentity(t *testing.T) {
	fixture := testserver.New(t, testserver.Options{})
	servers := []config.Server{
		{Name: "first", URL: fixture.URL},
		{Name: "second", URL: fixture.URL},
	}
	for _, test := range []struct {
		name        string
		config      *config.Config
		wantName    string
		wantVersion string
	}{
		{name: "defaults", config: &config.Config{Servers: servers}, wantName: "mmmcp", wantVersion: "dev"},
		{name: "configured", config: &config.Config{Name: "company-mcp", Version: "2.3.4", Servers: servers}, wantName: "company-mcp", wantVersion: "2.3.4"},
	} {
		t.Run(test.name, func(t *testing.T) {
			info := frontendServerInfo(t, test.config)
			if info.Name != test.wantName || info.Version != test.wantVersion {
				t.Fatalf("server info = %+v, want %s/%s", info, test.wantName, test.wantVersion)
			}
		})
	}
}

func TestHTTPContextConfigControlsServerIdentity(t *testing.T) {
	fixture := testserver.New(t, testserver.Options{})
	single := &config.Config{Servers: []config.Server{{Name: "single", URL: fixture.URL}}}
	multiple := &config.Config{
		Name: "selected-mcp", Version: "9.8.7",
		Servers: []config.Server{{Name: "first", URL: fixture.URL}, {Name: "second", URL: fixture.URL}},
	}
	for _, test := range []struct {
		name        string
		defaultCfg  *config.Config
		selectedCfg *config.Config
		wantName    string
		wantVersion string
	}{
		{name: "single selected", defaultCfg: multiple, selectedCfg: single, wantName: "mmmcp-test-component", wantVersion: "1.0.0"},
		{name: "multiple selected", defaultCfg: single, selectedCfg: multiple, wantName: "selected-mcp", wantVersion: "9.8.7"},
	} {
		t.Run(test.name, func(t *testing.T) {
			info := frontendServerInfo(t, test.defaultCfg, test.selectedCfg)
			if info.Name != test.wantName || info.Version != test.wantVersion {
				t.Fatalf("server info = %+v, want %s/%s", info, test.wantName, test.wantVersion)
			}
		})
	}
}

func frontendServerInfo(t *testing.T, cfg *config.Config, selected ...*config.Config) *mcp.Implementation {
	t.Helper()
	composite, err := mmmcp.New(t.Context(), cfg, mmmcp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = composite.Close() })
	handler := composite.HTTPHandler()
	if len(selected) > 0 {
		base := handler
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			base.ServeHTTP(w, r.WithContext(mmmcp.ContextWithConfig(r.Context(), selected[0])))
		})
	}
	frontend := httptest.NewServer(handler)
	t.Cleanup(frontend.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "identity-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: frontend.URL, HTTPClient: frontend.Client(), DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	initialized := session.InitializeResult()
	if initialized == nil || initialized.ServerInfo == nil {
		t.Fatal("frontend returned no server info")
	}
	return initialized.ServerInfo
}
