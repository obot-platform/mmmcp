// Package catalog compiles immutable composite feature snapshots and routes.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Catalog is an immutable, sorted composite feature snapshot.
type Catalog struct {
	id                string
	serverInfo        *mcp.Implementation
	tools             []*mcp.Tool
	prompts           []*mcp.Prompt
	resources         []*mcp.Resource
	resourceTemplates []*mcp.ResourceTemplate
	toolRoutes        map[string]ToolRoute
	promptRoutes      map[string]PromptRoute
	resourceRoutes    map[string]ResourceRoute
	templateRoutes    []ResourceTemplateRoute
}

// ServerInfo returns the single component's MCP implementation metadata.
func (c *Catalog) ServerInfo() *mcp.Implementation {
	if c == nil || c.serverInfo == nil {
		return nil
	}
	return &mcp.Implementation{Name: c.serverInfo.Name, Version: c.serverInfo.Version}
}

// Tools returns a copy of the sorted tool slice.
func (c *Catalog) Tools() []*mcp.Tool { return append([]*mcp.Tool(nil), c.tools...) }

// Prompts returns a copy of the sorted prompt slice.
func (c *Catalog) Prompts() []*mcp.Prompt { return append([]*mcp.Prompt(nil), c.prompts...) }

// Resources returns a copy of the sorted resource slice.
func (c *Catalog) Resources() []*mcp.Resource { return append([]*mcp.Resource(nil), c.resources...) }

// ResourceTemplates returns a copy of the sorted resource-template slice.
func (c *Catalog) ResourceTemplates() []*mcp.ResourceTemplate {
	return append([]*mcp.ResourceTemplate(nil), c.resourceTemplates...)
}

// RouteTool resolves an exposed tool name.
func (c *Catalog) RouteTool(name string) (ToolRoute, bool) {
	route, ok := c.toolRoutes[name]
	return route, ok
}

// RoutePrompt resolves an exposed prompt name.
func (c *Catalog) RoutePrompt(name string) (PromptRoute, bool) {
	route, ok := c.promptRoutes[name]
	return route, ok
}

// RouteResource resolves an exposed resource URI, including template expansions.
func (c *Catalog) RouteResource(uri string) (ResourceRoute, bool) {
	if route, ok := c.resourceRoutes[uri]; ok {
		return route, true
	}
	for _, route := range c.templateRoutes {
		if original, ok := route.toOriginal(uri); ok {
			return ResourceRoute{Component: route.Component, Prefix: route.Prefix, OriginalURI: original}, true
		}
	}
	return ResourceRoute{}, false
}

func newCatalog(c *Catalog) (*Catalog, error) {
	sort.Slice(c.tools, func(i, j int) bool { return c.tools[i].Name < c.tools[j].Name })
	sort.Slice(c.prompts, func(i, j int) bool { return c.prompts[i].Name < c.prompts[j].Name })
	sort.Slice(c.resources, func(i, j int) bool { return c.resources[i].URI < c.resources[j].URI })
	sort.Slice(c.resourceTemplates, func(i, j int) bool {
		return c.resourceTemplates[i].URITemplate < c.resourceTemplates[j].URITemplate
	})
	snapshot, err := json.Marshal(struct {
		Tools             []*mcp.Tool             `json:"tools"`
		Prompts           []*mcp.Prompt           `json:"prompts"`
		Resources         []*mcp.Resource         `json:"resources"`
		ResourceTemplates []*mcp.ResourceTemplate `json:"resourceTemplates"`
	}{c.tools, c.prompts, c.resources, c.resourceTemplates})
	if err != nil {
		return nil, fmt.Errorf("catalog snapshot: %w", err)
	}
	sum := sha256.Sum256(snapshot)
	c.id = hex.EncodeToString(sum[:16])
	return c, nil
}

func toolNames(values []*mcp.Tool) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.Name
	}
	return result
}

func promptNames(values []*mcp.Prompt) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.Name
	}
	return result
}

func resourceURIs(values []*mcp.Resource) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.URI
	}
	return result
}

func templateURIs(values []*mcp.ResourceTemplate) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = value.URITemplate
	}
	return result
}
