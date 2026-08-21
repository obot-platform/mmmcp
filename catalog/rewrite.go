package catalog

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	return "mmmcp+" + prefix + ":" + original
}

// CompositeURI maps a component resource URI into its exposed identity.
func (c *Catalog) CompositeURI(prefix, original string) string {
	return c.toCompositeURI(prefix, original)
}
