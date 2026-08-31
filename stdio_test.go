package mmmcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/storage"
	"github.com/obot-platform/mmmcp/testserver"
)

func TestRunPersistentUsesProcessScopedContextConfig(t *testing.T) {
	defaultFixture := testserver.New(t, testserver.Options{Tools: []testserver.Tool{{
		Definition: &mcp.Tool{Name: "default", InputSchema: map[string]any{"type": "object"}},
		Handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		},
	}}})
	requestFixture := testserver.New(t, testserver.Options{Tools: []testserver.Tool{{
		Definition: &mcp.Tool{Name: "request", InputSchema: map[string]any{"type": "object"}},
		Handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		},
	}}})

	composite, err := New(t.Context(), &config.Config{Servers: []config.Server{{Name: "default", URL: defaultFixture.URL}}}, Options{
		Storage: storage.Options{DataDirectory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer composite.Close()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	runCtx, cancel := context.WithCancel(ContextWithConfig(t.Context(), &config.Config{
		Servers: []config.Server{{Name: "selected", URL: requestFixture.URL}},
	}))
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- composite.runPersistent(runCtx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != "request" {
		t.Fatalf("stdio tools = %+v", listed.Tools)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-runDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("RunStdio error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("persistent server did not stop after client close")
	}
}

func TestRunPersistentUsesProcessScopedServerIdentity(t *testing.T) {
	fixture := testserver.New(t, testserver.Options{})
	composite, err := New(t.Context(), &config.Config{Servers: []config.Server{{Name: "default", URL: fixture.URL}}}, Options{
		Storage: storage.Options{DataDirectory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer composite.Close()

	selected := &config.Config{
		Name: "selected-stdio", Version: "4.5.6",
		Servers: []config.Server{{Name: "first", URL: fixture.URL}, {Name: "second", URL: fixture.URL}},
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	runCtx, cancel := context.WithCancel(ContextWithConfig(t.Context(), selected))
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- composite.runPersistent(runCtx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-identity-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	initialized := session.InitializeResult()
	if initialized == nil || initialized.ServerInfo == nil {
		t.Fatal("frontend returned no server info")
	}
	if info := initialized.ServerInfo; info.Name != "selected-stdio" || info.Version != "4.5.6" {
		t.Fatalf("server info = %+v, want selected-stdio/4.5.6", info)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-runDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("RunStdio error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("persistent server did not stop after client close")
	}
}

func TestCloseStopsPersistentFrontend(t *testing.T) {
	fixture := testserver.New(t, testserver.Options{Tools: []testserver.Tool{{
		Definition: &mcp.Tool{Name: "tool", InputSchema: map[string]any{"type": "object"}},
		Handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		},
	}}})
	composite, err := New(t.Context(), &config.Config{Servers: []config.Server{{Name: "fixture", URL: fixture.URL}}}, Options{
		Storage: storage.Options{DataDirectory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	runDone := make(chan error, 1)
	go func() { runDone <- composite.runPersistent(t.Context(), serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-close-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- composite.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Composite.Close did not stop persistent frontend")
	}
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("persistent frontend did not return")
	}
	_ = session.Close()
}
