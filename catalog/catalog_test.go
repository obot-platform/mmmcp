package catalog_test

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	componenthttp "github.com/obot-platform/mmmcp/component/http"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/testserver"
)

type staticDiscoverer struct {
	tools map[string][]*mcp.Tool
}

func TestCompileExhaustsPaginationSortsAndRoutesOriginalNames(t *testing.T) {
	fixture := testserver.New(t, testserver.Options{
		PageSize: 1,
		Tools: []testserver.Tool{
			{Definition: tool("zebra"), Handler: textHandler("zebra")},
			{Definition: tool("alpha"), Handler: textHandler("alpha")},
			{Definition: tool("middle"), Handler: textHandler("middle")},
		},
	})
	cfg := &config.Config{Servers: []config.Server{{Name: "local files", URL: fixture.URL}}}

	compiled, err := catalog.Compile(t.Context(), cfg, componenthttp.NewFactory(componenthttp.FactoryOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	tools := compiled.Tools()
	gotNames := make([]string, len(tools))
	for i, tool := range tools {
		gotNames[i] = tool.Name
	}
	wantNames := []string{"alpha", "middle", "zebra"}
	if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("tool names = %v, want %v", gotNames, wantNames)
	}
	for _, name := range wantNames {
		route, ok := compiled.RouteTool(name)
		if !ok {
			t.Fatalf("RouteTool(%q) not found", name)
		}
		if route.Component.Name != "local files" || route.OriginalName != name {
			t.Fatalf("RouteTool(%q) = %+v", name, route)
		}
	}
}

func TestCompileRejectsFinalCollision(t *testing.T) {
	discoverer := staticDiscoverer{tools: map[string][]*mcp.Tool{
		"first":  {tool("same")},
		"second": {tool("same")},
	}}
	cfg := &config.Config{Servers: []config.Server{
		{Name: "first", Prefix: "shared", URL: "https://first.invalid"},
		{Name: "second", Prefix: "shared", URL: "https://second.invalid"},
	}}
	_, err := catalog.Compile(t.Context(), cfg, discoverer)
	if err == nil || !strings.Contains(err.Error(), `"shared__same"`) || !strings.Contains(err.Error(), `"first"`) || !strings.Contains(err.Error(), `"second"`) {
		t.Fatalf("Compile error = %v, want collision details", err)
	}
}

func TestCompileDoesNotMutateDiscoveredTool(t *testing.T) {
	original := tool("read")
	discoverer := staticDiscoverer{tools: map[string][]*mcp.Tool{"files": {original}}}
	compiled, err := catalog.Compile(t.Context(), &config.Config{Servers: []config.Server{{Name: "files", URL: "https://example.invalid"}}}, discoverer)
	if err != nil {
		t.Fatal(err)
	}
	if original.Name != "read" {
		t.Fatalf("original tool name mutated to %q", original.Name)
	}
	if compiled.Tools()[0].Name != "read" {
		t.Fatalf("compiled tool name = %q", compiled.Tools()[0].Name)
	}
}

func (d staticDiscoverer) Discover(_ context.Context, server config.Server) (*component.Features, error) {
	return &component.Features{Tools: d.tools[server.Name]}, nil
}

func tool(name string) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: name + " description", InputSchema: map[string]any{"type": "object"}}
}

func textHandler(text string) mcp.ToolHandler {
	return func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
	}
}
