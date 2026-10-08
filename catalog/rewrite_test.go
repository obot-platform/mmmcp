package catalog_test

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

func TestRewriteClonesTypedResourceFieldsWithoutTraversingStructuredJSON(t *testing.T) {
	compiled, err := catalog.Compile(t.Context(), &config.Config{Servers: []config.Server{{Name: "files", URL: "https://example.invalid"}}}, featureDiscoverer{features: map[string]*component.Features{"files": {
		Resources:         []*mcp.Resource{{URI: "file:///known", Name: "known"}},
		ResourceTemplates: []*mcp.ResourceTemplate{{URITemplate: "file:///{path}", Name: "files"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	originalLink := &mcp.ResourceLink{URI: "file:///known", Name: "known"}
	originalEmbedded := &mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file:///dynamic.txt", Text: "data"}}
	structured := map[string]any{"uri": "file:///known"}
	original := &mcp.CallToolResult{Content: []mcp.Content{originalLink, originalEmbedded}, StructuredContent: structured}

	rewritten := compiled.RewriteCallToolResult("files", original)
	if rewritten == original || rewritten.Content[0] == originalLink || rewritten.Content[1] == originalEmbedded {
		t.Fatal("result or typed content was not cloned")
	}
	if got := rewritten.Content[0].(*mcp.ResourceLink).URI; got != "file://files//known" {
		t.Fatalf("link URI = %q", got)
	}
	if got := rewritten.Content[1].(*mcp.EmbeddedResource).Resource.URI; got != "file://files//dynamic.txt" {
		t.Fatalf("embedded URI = %q", got)
	}
	if originalLink.URI != "file:///known" || originalEmbedded.Resource.URI != "file:///dynamic.txt" {
		t.Fatal("original content was mutated")
	}
	if rewritten.StructuredContent.(map[string]any)["uri"] != "file:///known" {
		t.Fatal("structured content was traversed")
	}

	readOriginal := &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "file:///known", Text: "data"}}}
	readRewritten := compiled.RewriteReadResourceResult("files", readOriginal)
	if readRewritten.Contents[0].URI != "file://files//known" || readOriginal.Contents[0].URI != "file:///known" {
		t.Fatalf("read rewrite = %+v, original = %+v", readRewritten, readOriginal)
	}
}
