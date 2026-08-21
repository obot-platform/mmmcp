package catalog_test

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

func TestCompileAllFeaturesAppliesOverridesBeforeNamespace(t *testing.T) {
	inputSchema := map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: true}
	discoverer := featureDiscoverer{features: map[string]*component.Features{"Fancy Server": {
		Tools: []*mcp.Tool{
			{Name: "disabled", Description: "hidden", InputSchema: map[string]any{"type": "object"}},
			{Name: "search", Description: "live", InputSchema: inputSchema, Annotations: annotations},
		},
		Prompts:           []*mcp.Prompt{{Name: "explain", Description: "live prompt"}},
		Resources:         []*mcp.Resource{{URI: "file:///notes", Name: "notes", Description: "live resource"}},
		ResourceTemplates: []*mcp.ResourceTemplate{{URITemplate: "file:///{path}", Name: "files", Description: "live template"}},
	}}}
	cfg := &config.Config{Servers: []config.Server{{
		Name: "Fancy Server", Prefix: "fancy_server", URL: "https://example.invalid",
		Tools: []config.ToolOverride{
			{Name: "search", OverrideName: "find", OverrideDescription: "overridden tool", Enabled: true},
			{Name: "disabled", Enabled: false},
		},
		Prompts:           []config.PromptOverride{{Name: "explain", OverrideName: "describe", OverrideDescription: "overridden prompt", Enabled: true}},
		Resources:         []config.ResourceOverride{{URI: "file:///notes", OverrideURI: "file:///public", OverrideName: "public notes", OverrideDescription: "overridden resource", Enabled: true}},
		ResourceTemplates: []config.ResourceTemplateOverride{{URITemplate: "file:///{path}", OverrideURITemplate: "file:///public/{path}", OverrideName: "public files", OverrideDescription: "overridden template", Enabled: true}},
	}}}

	compiled, err := catalog.Compile(t.Context(), cfg, discoverer)
	if err != nil {
		t.Fatal(err)
	}
	if got := compiled.Tools(); len(got) != 1 || got[0].Name != "fancy_server__find" || got[0].Description != "overridden tool" {
		t.Fatalf("tools = %+v", got)
	} else if got[0].InputSchema == nil || got[0].Annotations != annotations {
		t.Fatal("tool schema or annotations were not preserved")
	}
	if got := compiled.Prompts(); len(got) != 1 || got[0].Name != "fancy_server__describe" || got[0].Description != "overridden prompt" {
		t.Fatalf("prompts = %+v", got)
	}
	if got := compiled.Resources(); len(got) != 1 || got[0].URI != "mmmcp+fancy_server:file:///public" || got[0].Name != "public notes" || got[0].Description != "overridden resource" {
		t.Fatalf("resources = %+v", got)
	}
	if got := compiled.ResourceTemplates(); len(got) != 1 || got[0].URITemplate != "mmmcp+fancy_server:file:///public/{path}" || got[0].Name != "public files" || got[0].Description != "overridden template" {
		t.Fatalf("resource templates = %+v", got)
	}
	if route, ok := compiled.RouteTool("fancy_server__find"); !ok || route.OriginalName != "search" {
		t.Fatalf("tool route = %+v, %v", route, ok)
	}
	if route, ok := compiled.RoutePrompt("fancy_server__describe"); !ok || route.OriginalName != "explain" {
		t.Fatalf("prompt route = %+v, %v", route, ok)
	}
	if route, ok := compiled.RouteResource("mmmcp+fancy_server:file:///public"); !ok || route.OriginalURI != "file:///notes" {
		t.Fatalf("resource route = %+v, %v", route, ok)
	}
	if route, ok := compiled.RouteResource("mmmcp+fancy_server:file:///public/report.txt"); !ok || route.OriginalURI != "file:///report.txt" {
		t.Fatalf("template route = %+v, %v", route, ok)
	}
}

func TestCompileSingleServerPreservesFeatureIdentities(t *testing.T) {
	discoverer := featureDiscoverer{features: map[string]*component.Features{"only": {
		Tools:             []*mcp.Tool{{Name: "search", InputSchema: map[string]any{"type": "object"}}},
		Prompts:           []*mcp.Prompt{{Name: "explain"}},
		Resources:         []*mcp.Resource{{URI: "file:///notes", Name: "notes"}},
		ResourceTemplates: []*mcp.ResourceTemplate{{URITemplate: "file:///{path}", Name: "files"}},
	}}}
	cfg := &config.Config{Servers: []config.Server{{Name: "only", URL: "https://example.invalid"}}}

	compiled, err := catalog.Compile(t.Context(), cfg, discoverer)
	if err != nil {
		t.Fatal(err)
	}
	if got := compiled.Tools(); len(got) != 1 || got[0].Name != "search" {
		t.Fatalf("tools = %+v", got)
	}
	if got := compiled.Prompts(); len(got) != 1 || got[0].Name != "explain" {
		t.Fatalf("prompts = %+v", got)
	}
	if got := compiled.Resources(); len(got) != 1 || got[0].URI != "file:///notes" {
		t.Fatalf("resources = %+v", got)
	}
	if got := compiled.ResourceTemplates(); len(got) != 1 || got[0].URITemplate != "file:///{path}" {
		t.Fatalf("resource templates = %+v", got)
	}
	if route, ok := compiled.RouteTool("search"); !ok || route.OriginalName != "search" || route.Prefix != "" {
		t.Fatalf("tool route = %+v, %v", route, ok)
	}
	if route, ok := compiled.RoutePrompt("explain"); !ok || route.OriginalName != "explain" || route.Prefix != "" {
		t.Fatalf("prompt route = %+v, %v", route, ok)
	}
	if route, ok := compiled.RouteResource("file:///notes"); !ok || route.OriginalURI != "file:///notes" || route.Prefix != "" {
		t.Fatalf("resource route = %+v, %v", route, ok)
	}
	if route, ok := compiled.RouteResource("file:///report.txt"); !ok || route.OriginalURI != "file:///report.txt" || route.Prefix != "" {
		t.Fatalf("template route = %+v, %v", route, ok)
	}
}

func TestCompileMultipleServersAddsPrefixes(t *testing.T) {
	discoverer := featureDiscoverer{features: map[string]*component.Features{
		"First Server":  {Tools: []*mcp.Tool{{Name: "search", InputSchema: map[string]any{"type": "object"}}}},
		"Second Server": {Tools: []*mcp.Tool{{Name: "search", InputSchema: map[string]any{"type": "object"}}}},
	}}
	cfg := &config.Config{Servers: []config.Server{
		{Name: "First Server", URL: "https://first.invalid"},
		{Name: "Second Server", URL: "https://second.invalid"},
	}}

	compiled, err := catalog.Compile(t.Context(), cfg, discoverer)
	if err != nil {
		t.Fatal(err)
	}
	got := compiled.Tools()
	if len(got) != 2 || got[0].Name != "first_server__search" || got[1].Name != "second_server__search" {
		t.Fatalf("tools = %+v", got)
	}
}

func TestCompileRejectsInvalidOverridesAndFinalCollisions(t *testing.T) {
	features := &component.Features{
		Tools:     []*mcp.Tool{{Name: "one", InputSchema: map[string]any{"type": "object"}}, {Name: "two", InputSchema: map[string]any{"type": "object"}}},
		Prompts:   []*mcp.Prompt{{Name: "prompt"}},
		Resources: []*mcp.Resource{{URI: "file:///one", Name: "one"}, {URI: "file:///two", Name: "two"}},
	}
	tests := []struct {
		name      string
		overrides config.Server
		want      string
	}{
		{"duplicate override identity", config.Server{Tools: []config.ToolOverride{{Name: "one", Enabled: true}, {Name: "one", Enabled: true}}}, "duplicate tool override"},
		{"tool final collision", config.Server{Tools: []config.ToolOverride{{Name: "one", OverrideName: "same", Enabled: true}, {Name: "two", OverrideName: "same", Enabled: true}}}, "tool name"},
		{"resource final collision", config.Server{Resources: []config.ResourceOverride{{URI: "file:///one", OverrideURI: "file:///same", Enabled: true}, {URI: "file:///two", OverrideURI: "file:///same", Enabled: true}}}, "resource URI"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := test.overrides
			server.Name, server.URL = "fixture", "https://example.invalid"
			_, err := catalog.Compile(t.Context(), &config.Config{Servers: []config.Server{server}}, featureDiscoverer{features: map[string]*component.Features{"fixture": features}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Compile error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCompileIgnoresOverridesForUndiscoveredFeatures(t *testing.T) {
	features := &component.Features{
		Tools:             []*mcp.Tool{{Name: "tool", InputSchema: map[string]any{"type": "object"}}},
		Prompts:           []*mcp.Prompt{{Name: "prompt"}},
		Resources:         []*mcp.Resource{{URI: "file:///resource", Name: "resource"}},
		ResourceTemplates: []*mcp.ResourceTemplate{{URITemplate: "file:///{path}", Name: "template"}},
	}
	server := config.Server{
		Name:              "fixture",
		URL:               "https://example.invalid",
		Tools:             []config.ToolOverride{{Name: "missing", Enabled: true}},
		Prompts:           []config.PromptOverride{{Name: "missing", Enabled: true}},
		Resources:         []config.ResourceOverride{{URI: "file:///missing", Enabled: true}},
		ResourceTemplates: []config.ResourceTemplateOverride{{URITemplate: "file:///missing/{path}", Enabled: true}},
	}

	compiled, err := catalog.Compile(t.Context(), &config.Config{Servers: []config.Server{server}}, featureDiscoverer{features: map[string]*component.Features{"fixture": features}})
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Tools()) != 1 || len(compiled.Prompts()) != 1 || len(compiled.Resources()) != 1 || len(compiled.ResourceTemplates()) != 1 {
		t.Fatal("discovered features were not preserved")
	}
}

type featureDiscoverer struct {
	features map[string]*component.Features
}

func (d featureDiscoverer) Discover(_ context.Context, server config.Server) (*component.Features, error) {
	return d.features[server.Name], nil
}
