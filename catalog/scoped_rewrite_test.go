package catalog_test

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

func TestScopedDiscoveryPreservesResourceIdentities(t *testing.T) {
	for _, family := range []string{"tools", "prompts"} {
		for _, prefix := range []string{"", "files"} {
			t.Run(family+"/"+prefix, func(t *testing.T) {
				server := config.Server{
					Name:            "files",
					Prefix:          prefix,
					DiscoveryMethod: family,
					Resources: []config.ResourceOverride{{
						URI:         "file:///notes",
						OverrideURI: "file:///renamed",
						Enabled:     true,
					}},
					ResourceTemplates: []config.ResourceTemplateOverride{{
						URITemplate:         "file:///dynamic/{id}",
						OverrideURITemplate: "file:///exposed/{id}",
						Enabled:             true,
					}},
				}
				// No resource inventory is supplied to the tool/prompt discovery pass.
				scoped, err := catalog.Compile(t.Context(), &config.Config{Servers: []config.Server{server}}, featureDiscoverer{features: map[string]*component.Features{"files": {}}})
				if err != nil {
					t.Fatal(err)
				}

				server.DiscoveryMethod = "resources"
				full, err := catalog.Compile(t.Context(), &config.Config{Servers: []config.Server{server}}, featureDiscoverer{features: map[string]*component.Features{"files": {
					Resources:         []*mcp.Resource{{URI: "file:///notes"}, {URI: "file:///plain"}},
					ResourceTemplates: []*mcp.ResourceTemplate{{URITemplate: "file:///dynamic/{id}"}},
				}}})
				if err != nil {
					t.Fatal(err)
				}

				for _, uri := range []string{"file:///notes", "file:///plain", "file:///dynamic/123"} {
					content := &mcp.ResourceLink{URI: uri}
					tool := scoped.RewriteCallToolResult(prefix, &mcp.CallToolResult{Content: []mcp.Content{content}})
					prompt := scoped.RewriteGetPromptResult(prefix, &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Content: content}}})
					for _, rewritten := range []string{tool.Content[0].(*mcp.ResourceLink).URI, prompt.Messages[0].Content.(*mcp.ResourceLink).URI} {
						route, ok := full.RouteResource(rewritten)
						if !ok || route.OriginalURI != uri {
							t.Fatalf("rewritten %q cannot route back to %q: %+v", rewritten, uri, route)
						}
					}
				}
			})
		}
	}
}
