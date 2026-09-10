package mmmcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
)

func TestDynamicToolChangesNotifyOnNextRequest(t *testing.T) {
	for _, protocol := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(protocol, func(t *testing.T) { testDynamicToolChanges(t, protocol) })
	}
}

func testDynamicToolChanges(t *testing.T, protocol string) {
	t.Helper()
	stateless := protocol == "2026-07-28"
	request := func(session *mcp.ClientSession) error {
		if stateless {
			_, err := session.ListPrompts(t.Context(), &mcp.ListPromptsParams{})
			return err
		}
		return session.Ping(t.Context(), nil)
	}
	fixture := namedToolFixture(t, "fixture")
	var current atomic.Pointer[config.Config]
	setConfig := func(name, description string, enabled bool, version string) {
		current.Store(&config.Config{Version: version, Servers: []config.Server{{
			Name: "fixture", URL: fixture.URL,
			Tools: []config.ToolOverride{{Name: "tool", OverrideName: name, OverrideDescription: description, Enabled: enabled}},
		}}})
	}
	setConfig("first", "original", true, "1")
	composite, err := mmmcp.New(t.Context(), current.Load(), mmmcp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer composite.Close()
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(mmmcp.ContextWithConfigID(r.Context(), r.URL.Path))
		r = r.WithContext(mmmcp.ContextWithConfig(r.Context(), current.Load()))
		composite.HTTPHandler().ServeHTTP(w, r)
	}))
	defer frontend.Close()
	connect := func(path string) (*mcp.ClientSession, chan struct{}) {
		changed := make(chan struct{}, 10)
		client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, &mcp.ClientOptions{
			ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
		})
		session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: frontend.URL + path, HTTPClient: frontend.Client()}, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
		if err != nil {
			t.Fatal(err)
		}
		return session, changed
	}
	session, changed := connect("/shared")
	defer session.Close()
	other, otherChanged := connect("/shared")
	defer other.Close()
	isolated, isolatedChanged := connect("/isolated")
	defer isolated.Close()
	assertOnlyTool(t, session, "first")
	assertOnlyTool(t, other, "first")
	checkNotification := func(ch chan struct{}, want bool) {
		t.Helper()
		timeout := 100 * time.Millisecond
		if want {
			timeout = 5 * time.Second
		}
		select {
		case <-ch:
			if !want {
				t.Fatal("unexpected tool list changed notification")
			}
		case <-time.After(timeout):
			if want {
				t.Fatal("missing tool list changed notification")
			}
		}
	}
	for _, step := range []struct {
		name        string
		description string
		enabled     bool
		version     string
		list        bool
		notify      bool
	}{
		{
			name:        "first",
			description: "original",
			enabled:     true,
			version:     "1",
			list:        false,
			notify:      false,
		},
		{
			name:        "renamed",
			description: "original",
			enabled:     true,
			version:     "1",
			list:        false,
			notify:      true,
		},
		{
			name:        "renamed",
			description: "original",
			enabled:     true,
			version:     "1",
			list:        false,
			notify:      false,
		},
		{
			name:        "renamed",
			description: "updated",
			enabled:     true,
			version:     "1",
			list:        false,
			notify:      true,
		},
		{
			name:        "renamed",
			description: "updated",
			enabled:     false,
			version:     "1",
			list:        false,
			notify:      true,
		},
		{
			name:        "listed",
			description: "updated",
			enabled:     true,
			version:     "1",
			list:        true,
			notify:      false,
		},
		{
			name:        "listed",
			description: "updated",
			enabled:     true,
			version:     "1",
			list:        false,
			notify:      false,
		},
		{
			name:        "listed",
			description: "updated",
			enabled:     true,
			version:     "2",
			list:        false,
			notify:      false,
		},
	} {
		setConfig(step.name, step.description, step.enabled, step.version)
		if step.list {
			assertOnlyTool(t, session, step.name)
		} else if err := request(session); err != nil {
			t.Fatal(err)
		}
		want := step.notify || (stateless && step.list)
		checkNotification(changed, want)
		if stateless {
			checkNotification(otherChanged, want)
		}
	}
	checkNotification(isolatedChanged, false)
	checkNotification(otherChanged, false)
	if err := request(other); err != nil {
		t.Fatal(err)
	}
	checkNotification(otherChanged, !stateless)
}
