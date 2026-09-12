package catalog_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

type featureDiscoverer struct {
	features map[string]*component.Features
}

type gatedDiscoverer struct {
	started chan string
	release chan struct{}
	active  atomic.Int32
	maximum atomic.Int32
}

type failFastDiscoverer struct {
	blockedStarted  chan struct{}
	blockedCanceled chan struct{}
	failure         error
	nilFeatures     bool
	malformed       bool
}

func (d *failFastDiscoverer) Discover(ctx context.Context, server config.Server) (*component.Features, error) {
	switch server.Name {
	case "blocked":
		close(d.blockedStarted)
		<-ctx.Done()
		close(d.blockedCanceled)
		return nil, ctx.Err()
	case "failing":
		<-d.blockedStarted
		if d.nilFeatures {
			return nil, nil
		}
		if d.malformed {
			return &component.Features{Tools: []*mcp.Tool{nil}}, nil
		}
		return nil, d.failure
	default:
		return &component.Features{}, nil
	}
}

func (d *gatedDiscoverer) Discover(ctx context.Context, server config.Server) (*component.Features, error) {
	active := d.active.Add(1)
	for {
		maximum := d.maximum.Load()
		if active <= maximum || d.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	d.started <- server.Name
	select {
	case <-d.release:
	case <-ctx.Done():
		d.active.Add(-1)
		return nil, ctx.Err()
	}
	d.active.Add(-1)
	return &component.Features{Tools: []*mcp.Tool{{Name: "tool", InputSchema: map[string]any{"type": "object"}}}}, nil
}

func TestCompileDiscoversComponentsInBoundedParallel(t *testing.T) {
	const parallelism = 8
	discoverer := &gatedDiscoverer{
		started: make(chan string, parallelism),
		release: make(chan struct{}),
	}
	defer func() {
		select {
		case <-discoverer.release:
		default:
			close(discoverer.release)
		}
	}()

	servers := make([]config.Server, parallelism+2)
	for i := range servers {
		servers[i] = config.Server{Name: string(rune('a' + i)), URL: "https://example.invalid"}
	}
	done := make(chan error, 1)
	go func() {
		_, err := catalog.Compile(t.Context(), &config.Config{Servers: servers}, discoverer)
		done <- err
	}()

	for range parallelism {
		select {
		case <-discoverer.started:
		case <-time.After(time.Second):
			t.Fatal("component discoveries did not start in parallel")
		}
	}
	if got := discoverer.maximum.Load(); got != parallelism {
		t.Fatalf("maximum parallel discoveries = %d, want %d", got, parallelism)
	}
	close(discoverer.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("catalog compilation did not finish")
	}
}

func TestCompileCancelsBlockedDiscoveryAfterInvalidComponentResult(t *testing.T) {
	failure := errors.New("discovery failed")
	for _, test := range []struct {
		name        string
		nilFeatures bool
		malformed   bool
		want        string
	}{
		{name: "error"},
		{name: "nil features", nilFeatures: true, want: `component "failing" returned nil features`},
		{name: "malformed features", malformed: true, want: `component "failing" returned a nil tool`},
	} {
		t.Run(test.name, func(t *testing.T) {
			discoverer := &failFastDiscoverer{
				blockedStarted:  make(chan struct{}),
				blockedCanceled: make(chan struct{}),
				failure:         failure,
				nilFeatures:     test.nilFeatures,
				malformed:       test.malformed,
			}
			done := make(chan error, 1)
			go func() {
				_, err := catalog.Compile(t.Context(), &config.Config{Servers: []config.Server{
					{Name: "blocked", URL: "https://blocked.invalid"},
					{Name: "failing", URL: "https://failing.invalid"},
				}}, discoverer)
				done <- err
			}()

			select {
			case err := <-done:
				if test.want != "" {
					if err == nil || !strings.Contains(err.Error(), test.want) {
						t.Fatalf("Compile error = %v, want %q", err, test.want)
					}
				} else if !errors.Is(err, failure) {
					t.Fatalf("Compile error = %v, want %v", err, failure)
				}
			case <-time.After(time.Second):
				t.Fatal("catalog compilation did not fail fast")
			}
			select {
			case <-discoverer.blockedCanceled:
			case <-time.After(time.Second):
				t.Fatal("blocked discovery was not canceled")
			}
		})
	}
}

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
	if route, ok := compiled.RouteTool("fancy_server__find"); !ok || route.Tool.Name != "search" {
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
	if route, ok := compiled.RouteTool("search"); !ok || route.Tool.Name != "search" || route.Prefix != "" {
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
	if len(compiled.Tools()) != 0 || len(compiled.Prompts()) != 1 || len(compiled.Resources()) != 1 || len(compiled.ResourceTemplates()) != 1 {
		t.Fatal("tools without overrides must be hidden; other discovered features must be preserved")
	}
}

func (d featureDiscoverer) Discover(_ context.Context, server config.Server) (*component.Features, error) {
	return d.features[server.Name], nil
}
