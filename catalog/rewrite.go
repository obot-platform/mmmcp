package catalog

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/namespace"
)

// RewriteCallToolResult clones a tool result and rewrites supported resource identities.
func (c *Catalog) RewriteCallToolResult(prefix string, result *mcp.CallToolResult) *mcp.CallToolResult {
	if result == nil {
		return nil
	}
	clone := *result
	clone.Content = c.rewriteContent(prefix, result.Content)
	return &clone
}

// RewriteGetPromptResult clones a prompt result and rewrites supported resource identities.
func (c *Catalog) RewriteGetPromptResult(prefix string, result *mcp.GetPromptResult) *mcp.GetPromptResult {
	if result == nil {
		return nil
	}
	clone := *result
	clone.Messages = make([]*mcp.PromptMessage, len(result.Messages))
	for i, message := range result.Messages {
		if message == nil {
			continue
		}
		messageClone := *message
		messageClone.Content = c.rewriteOneContent(prefix, message.Content)
		clone.Messages[i] = &messageClone
	}
	return &clone
}

// RewriteReadResourceResult clones a read result and rewrites content URIs.
func (c *Catalog) RewriteReadResourceResult(prefix string, result *mcp.ReadResourceResult) *mcp.ReadResourceResult {
	if result == nil {
		return nil
	}
	clone := *result
	clone.Contents = make([]*mcp.ResourceContents, len(result.Contents))
	for i, contents := range result.Contents {
		if contents == nil {
			continue
		}
		contentsClone := *contents
		contentsClone.URI = c.toCompositeURI(prefix, contents.URI)
		clone.Contents[i] = &contentsClone
	}
	return &clone
}

func (c *Catalog) rewriteContent(prefix string, values []mcp.Content) []mcp.Content {
	if values == nil {
		return nil
	}
	result := make([]mcp.Content, len(values))
	for i, value := range values {
		result[i] = c.rewriteOneContent(prefix, value)
	}
	return result
}

func (c *Catalog) rewriteOneContent(prefix string, value mcp.Content) mcp.Content {
	switch content := value.(type) {
	case *mcp.ResourceLink:
		if content == nil {
			return content
		}
		clone := *content
		clone.URI = c.toCompositeURI(prefix, content.URI)
		return &clone
	case *mcp.EmbeddedResource:
		if content == nil {
			return content
		}
		clone := *content
		if content.Resource != nil {
			resource := *content.Resource
			resource.URI = c.toCompositeURI(prefix, content.Resource.URI)
			clone.Resource = &resource
		}
		return &clone
	case *mcp.ToolResultContent: //nolint:staticcheck // Required to rewrite sampling results from legacy MCP versions.
		if content == nil {
			return content
		}
		clone := *content
		clone.Content = c.rewriteContent(prefix, content.Content)
		return &clone
	default:
		return value
	}
}

func (c *Catalog) toCompositeURI(prefix, original string) string {
	for composite, route := range c.resourceRoutes {
		if route.Prefix == prefix && route.OriginalURI == original {
			return composite
		}
	}
	for _, route := range c.templateRoutes {
		if route.Prefix == prefix {
			if composite, ok := route.toComposite(original); ok {
				return composite
			}
		}
	}
	if composite, err := namespace.Resource(prefix, original); err == nil {
		return composite
	}
	return original
}

// rewriteUIMeta points an MCP Apps tool's UI resource reference (_meta.ui.resourceUri and
// the legacy flat key "ui/resourceUri") at the resource's exposed identity, including any
// configured URI override, so a host reading the tool list can fetch the UI through the
// composite server. Resources and templates must be compiled before tools.
func (c *Catalog) rewriteUIMeta(prefix string, meta mcp.Meta) mcp.Meta {
	flat, hasFlat := meta["ui/resourceUri"].(string)
	ui, hasUI := meta["ui"].(map[string]any)
	if !hasFlat && !hasUI {
		return meta
	}
	clone := make(mcp.Meta, len(meta))
	for key, value := range meta {
		clone[key] = value
	}
	if hasFlat {
		clone["ui/resourceUri"] = c.toCompositeURI(prefix, flat)
	}
	if hasUI {
		uiClone := make(map[string]any, len(ui))
		for key, value := range ui {
			uiClone[key] = value
		}
		if uri, ok := ui["resourceUri"].(string); ok {
			uiClone["resourceUri"] = c.toCompositeURI(prefix, uri)
		}
		clone["ui"] = uiClone
	}
	return clone
}

// CompositeURI maps a component resource URI into its exposed identity.
func (c *Catalog) CompositeURI(prefix, original string) string {
	return c.toCompositeURI(prefix, original)
}
