package catalog_test

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

func TestServerToolPermissionsRestrictListingsAndRoutes(t *testing.T) {
	discoverer := featureDiscoverer{features: map[string]*component.Features{"server": {
		Tools:   []*mcp.Tool{{Name: "echo"}, {Name: "new_tool"}, {Name: "disabled"}},
		Prompts: []*mcp.Prompt{{Name: "prompt"}},
	}}}
	registry := catalog.NewRegistry(discoverer)
	defer registry.Close()
	for _, tc := range []struct {
		name      string
		disabled  bool
		overrides []config.ToolOverride
		want      int
	}{
		{
			name: "no overrides allows all",
			want: 3,
		},
		{
			name:     "disabled without overrides",
			disabled: true,
		},
		{
			name:      "only enabled overrides",
			overrides: []config.ToolOverride{{Name: "echo", OverrideName: "renamed", Enabled: true}, {Name: "disabled"}},
			want:      1,
		},
		{
			name:      "disabled overrides everything",
			disabled:  true,
			overrides: []config.ToolOverride{{Name: "echo", Enabled: true}},
		},
		{
			name:      "all overrides disabled",
			overrides: []config.ToolOverride{{Name: "echo"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Servers: []config.Server{{
					Name:         "server",
					Prefix:       "test",
					Tools:        tc.overrides,
					DisableTools: tc.disabled,
				}},
			}
			compiled, _, err := registry.Get(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if len(compiled.Tools()) != tc.want {
				t.Fatalf("tools = %v, want %d", compiled.Tools(), tc.want)
			}
			if len(compiled.Prompts()) != 1 {
				t.Fatal("tool restrictions must not hide prompts")
			}
			if _, ok := compiled.RouteTool("test__new_tool"); ok != (tc.want == 3) {
				t.Fatal("ungranted tool remained callable")
			}
			if _, ok := compiled.RouteTool("test__renamed"); ok != (tc.want == 1) {
				t.Fatal("echo route did not match grant")
			}
		})
	}
}
