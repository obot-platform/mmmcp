package catalog

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/toolsearch"
)

func (c *Catalog) searchDocuments() []toolsearch.Document {
	documents := make([]toolsearch.Document, 0, len(c.tools))
	for _, tool := range c.tools {
		route := c.toolRoutes[tool.Name]
		ref := toolsearch.Reference{Name: tool.Name}
		documents = append(documents, toolsearch.Document{
			ExposedName: tool.Name,
			Component:   route.Component.Name,
			Reference:   ref,
			Revision:    toolsearch.Revision(ref, route.Component.Name, route.Tool.Name, tool),
			Tool:        tool,
		})
	}
	return documents
}

// StartSearchIndex starts background indexing when tool search is enabled.
// The context controls the builder and its retries after this method returns.
func (c *Catalog) StartSearchIndex(ctx context.Context) {
	if c != nil && c.searchIndex != nil {
		c.searchIndex.Start(ctx)
	}
}

// StopSearchIndex cancels a pending background build. A completed index remains usable.
func (c *Catalog) StopSearchIndex() {
	if c != nil && c.searchIndex != nil {
		c.searchIndex.Stop()
	}
}

// SearchTool handles a search tool call and formats its result for MCP clients.
func (c *Catalog) SearchTool(ctx context.Context, arguments any) (*mcp.CallToolResult, error) {
	if c.searchIndex == nil {
		return nil, fmt.Errorf("tool search is disabled")
	}
	return c.searchIndex.Call(ctx, arguments)
}

// RouteReference returns the current route and revision for an exposed tool reference.
// It reports false when the tool is absent from this catalog.
func (c *Catalog) RouteReference(ref toolsearch.Reference) (ToolRoute, string, bool) {
	if c.searchIndex != nil {
		revision, ok := c.searchIndex.Revision(ref)
		if !ok {
			return ToolRoute{}, "", false
		}
		route, ok := c.toolRoutes[ref.Name]
		return route, revision, ok
	}
	// Direct-mode catalogs have no search index and do not make generic calls.
	for _, tool := range c.tools {
		if tool.Name == ref.Name {
			// Local tools, such as the authorization tool, have no downstream route.
			route, ok := c.toolRoutes[tool.Name]
			if !ok {
				return ToolRoute{}, "", false
			}
			return route, toolsearch.Revision(ref, route.Component.Name, route.Tool.Name, tool), true
		}
	}
	return ToolRoute{}, "", false
}
