package mmmcp

import (
	"encoding/json"
	"testing"

	"github.com/obot-platform/mmmcp/config"
)

func TestRequestCatalogSelectionSharedByDirectAndGenericCalls(t *testing.T) {
	for _, toolSearch := range []bool{false, true} {
		cfg := &config.Config{
			RequestScopedDiscovery: true,
			ToolSearch:             toolSearch,
			Servers: []config.Server{
				{
					Name:   "first",
					Prefix: "chosen_",
				},
				{
					Name: "unrelated",
				},
			},
		}
		name := "chosen_renamed"
		var arguments json.RawMessage
		if toolSearch {
			arguments = json.RawMessage(`{"name":"chosen_renamed","revision":"revision"}`)
			name = "call_tool"
		}

		composite := &Composite{}
		selected := composite.requestCatalogConfig(cfg, "tools/call", name, arguments)
		if len(selected.Servers) != 1 || selected.Servers[0].Name != "first" || selected.Servers[0].Prefix != "chosen_" || selected.Servers[0].DiscoveryMethod != "tools" {
			t.Fatalf("selection = %+v", selected.Servers)
		}

		if len(cfg.Servers) != 2 || cfg.Servers[0].DiscoveryMethod != "" {
			t.Fatal("mutated caller config")
		}

		prompts := composite.requestCatalogConfig(cfg, "prompts/list", "", nil)
		if len(prompts.Servers) != 2 || prompts.Servers[0].DiscoveryMethod != "prompts" {
			t.Fatalf("prompt selection = %+v", prompts.Servers)
		}
	}
}
