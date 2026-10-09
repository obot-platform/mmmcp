package mmmcp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/toolsearch"
)

type changingToolDiscoverer struct {
	mu             sync.Mutex
	tool           *mcp.Tool
	calls          int
	err            error
	refreshStarted chan struct{}
	refreshRelease <-chan struct{}
}

func (d *changingToolDiscoverer) Discover(ctx context.Context, _ config.Server) (*component.Features, error) {
	call, tool, started, release, err := func() (int, *mcp.Tool, chan struct{}, <-chan struct{}, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.calls++
		return d.calls, d.tool, d.refreshStarted, d.refreshRelease, d.err
	}()
	if call == 2 && started != nil {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return &component.Features{Tools: []*mcp.Tool{tool}}, nil
}

func (d *changingToolDiscoverer) setTool(tool *mcp.Tool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tool = tool
}

func (d *changingToolDiscoverer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *changingToolDiscoverer) setError(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.err = err
}

func TestResolveToolCallRefreshesStalePerRequestCatalog(t *testing.T) {
	discoverer := &changingToolDiscoverer{tool: &mcp.Tool{Name: "delete_file", InputSchema: map[string]any{"type": "object"}}}
	cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "component", URL: "https://example.invalid"}}}
	ingress := &Composite{registry: catalog.NewRegistry(discoverer), defaultConfig: &config.Config{}}
	owner := &Composite{registry: catalog.NewRegistry(discoverer), defaultConfig: &config.Config{}}
	defer ingress.registry.Close()
	defer owner.registry.Close()
	if _, _, err := ingress.registry.Get(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := owner.registry.Get(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}

	discoverer.setTool(&mcp.Tool{Name: "delete_file", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
	}})
	fresh, _, err := owner.registry.Refresh(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	reference := toolsearch.Reference{Name: "delete_file"}
	_, revision, ok := fresh.RouteReference(reference)
	if !ok {
		t.Fatal("owner did not publish the changed tool")
	}
	arguments, err := json.Marshal(toolsearch.CallArguments{Name: reference.Name, Revision: revision, Arguments: json.RawMessage(`{"path":"secret.txt"}`)})
	if err != nil {
		t.Fatal(err)
	}
	call, ok, err := ingress.ResolveToolCall(t.Context(), cfg, toolsearch.CallToolName, arguments)
	if err != nil || !ok || call.Route == nil || call.Result != nil || string(call.Arguments) != `{"path":"secret.txt"}` {
		t.Fatalf("ingress rejected owner's fresh reference: call=%#v ok=%t err=%v", call, ok, err)
	}
	if got := discoverer.count(); got != 4 {
		t.Fatalf("discoveries = %d, want initial catalogs plus one refresh each", got)
	}
}

func TestResolveToolCallDoesNotRefreshInvalidArguments(t *testing.T) {
	discoverer := &changingToolDiscoverer{tool: &mcp.Tool{Name: "delete_file", InputSchema: map[string]any{"type": "object"}}}
	cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "component", URL: "https://example.invalid"}}}
	composite := &Composite{registry: catalog.NewRegistry(discoverer)}
	defer composite.registry.Close()
	call, ok, err := composite.ResolveToolCall(t.Context(), cfg, toolsearch.CallToolName, json.RawMessage(`{"name":"delete_file"}`))
	if err != nil || !ok || call.Result == nil || !call.Result.IsError || call.Route != nil {
		t.Fatalf("invalid arguments result: call=%#v ok=%t err=%v", call, ok, err)
	}
	if got := discoverer.count(); got != 1 {
		t.Fatalf("discoveries = %d, want initial catalog only", got)
	}
}

func TestResolveToolCallBoundsRepeatedMisses(t *testing.T) {
	discoverer := &changingToolDiscoverer{tool: &mcp.Tool{Name: "delete_file", InputSchema: map[string]any{"type": "object"}}}
	cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "component", URL: "https://example.invalid"}}}
	composite := &Composite{registry: catalog.NewRegistry(discoverer)}
	defer composite.registry.Close()
	arguments := json.RawMessage(`{"name":"missing_tool","revision":"old"}`)
	for range 5 {
		call, ok, err := composite.ResolveToolCall(t.Context(), cfg, toolsearch.CallToolName, arguments)
		if err != nil || !ok || call.Result == nil || !call.Result.IsError || call.Route != nil {
			t.Fatalf("missing call: call=%#v ok=%t err=%v", call, ok, err)
		}
	}
	if got := discoverer.count(); got != 2 {
		t.Fatalf("discoveries = %d, want initial catalog and one fallback refresh", got)
	}

	changed := *cfg
	changed.Servers = append([]config.Server(nil), cfg.Servers...)
	changed.Servers[0].DiscoveryRevision = "new"
	_, _, err := composite.ResolveToolCall(t.Context(), &changed, toolsearch.CallToolName, arguments)
	if err != nil {
		t.Fatal(err)
	}
	if got := discoverer.count(); got != 4 {
		t.Fatalf("discoveries after config change = %d, want a fresh catalog and fallback refresh", got)
	}
}

func TestResolveToolCallCoalescesConcurrentMisses(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	discoverer := &changingToolDiscoverer{
		tool:           &mcp.Tool{Name: "delete_file", InputSchema: map[string]any{"type": "object"}},
		refreshStarted: started,
		refreshRelease: release,
	}
	cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "component", URL: "https://example.invalid"}}}
	composite := &Composite{registry: catalog.NewRegistry(discoverer)}
	defer composite.registry.Close()
	if _, _, err := composite.registry.Get(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	arguments := json.RawMessage(`{"name":"missing_tool","revision":"old"}`)
	const callers = 6
	results := make(chan error, callers)
	for range callers {
		go func() {
			call, ok, err := composite.ResolveToolCall(t.Context(), cfg, toolsearch.CallToolName, arguments)
			if err == nil && (!ok || call.Result == nil || !call.Result.IsError || call.Route != nil) {
				err = errors.New("missing call did not fail closed")
			}
			results <- err
		}()
	}
	<-started
	close(release)
	for range callers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := discoverer.count(); got != 2 {
		t.Fatalf("concurrent misses discovered %d times, want 2", got)
	}
}

func TestResolveToolCallKeepsSnapshotWhenRefreshFails(t *testing.T) {
	discoverer := &changingToolDiscoverer{tool: &mcp.Tool{Name: "delete_file", InputSchema: map[string]any{"type": "object"}}}
	cfg := &config.Config{ToolSearch: true, Servers: []config.Server{{Name: "component", URL: "https://example.invalid"}}}
	composite := &Composite{registry: catalog.NewRegistry(discoverer)}
	defer composite.registry.Close()
	current, _, err := composite.registry.Get(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, revision, ok := current.RouteReference(toolsearch.Reference{Name: "delete_file"})
	if !ok {
		t.Fatal("catalog did not publish the tool")
	}
	failure := errors.New("discovery unavailable")
	discoverer.setError(failure)
	missing, err := json.Marshal(toolsearch.CallArguments{Name: "new_tool", Revision: "new-revision"})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		call, ok, err := composite.ResolveToolCall(t.Context(), cfg, toolsearch.CallToolName, missing)
		if err != nil || !ok || call.Result == nil || !call.Result.IsError || call.Route != nil {
			t.Fatalf("failed refresh did not reject the missing tool: call=%#v ok=%t err=%v", call, ok, err)
		}
	}
	if got := discoverer.count(); got != 2 {
		t.Fatalf("discoveries = %d, want initial catalog and one failed refresh", got)
	}
	known, err := json.Marshal(toolsearch.CallArguments{Name: "delete_file", Revision: revision, Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	call, ok, err := composite.ResolveToolCall(t.Context(), cfg, toolsearch.CallToolName, known)
	if err != nil || !ok || call.Route == nil || call.Result != nil {
		t.Fatalf("failed refresh broke a known tool: call=%#v ok=%t err=%v", call, ok, err)
	}
}
