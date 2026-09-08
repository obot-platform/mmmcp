package catalog

import (
	"encoding/base64"
	"encoding/json"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	FamilyTools             Family = "tools"
	FamilyPrompts           Family = "prompts"
	FamilyResources         Family = "resources"
	FamilyResourceTemplates Family = "resourceTemplates"
)

// Family identifies one independently paginated feature collection.
type Family string

type cursor struct {
	Catalog string `json:"catalog"`
	Family  Family `json:"family"`
	After   string `json:"after"`
}

func (c *Catalog) page(family Family, identities []string, requested string, pageSize int) (int, int, string, error) {
	start := 0
	if requested != "" {
		decoded, err := decodeCursor(requested)
		if err != nil || decoded.Catalog != c.id || decoded.Family != family || decoded.After == "" {
			return 0, 0, "", invalidCursor()
		}
		start = sort.SearchStrings(identities, decoded.After)
		if start >= len(identities) || identities[start] != decoded.After {
			return 0, 0, "", invalidCursor()
		}
		start++
	}
	if pageSize <= 0 {
		pageSize = len(identities)
	}
	end := min(start+pageSize, len(identities))
	var next string
	if end < len(identities) {
		next = encodeCursor(cursor{Catalog: c.id, Family: family, After: identities[end-1]})
	}
	return start, end, next, nil
}

func encodeCursor(value cursor) string {
	data, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(value string) (cursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursor{}, err
	}
	var result cursor
	if err := json.Unmarshal(data, &result); err != nil {
		return cursor{}, err
	}
	return result, nil
}

func invalidCursor() error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "invalid or expired catalog cursor"}
}

// PageTools returns one stable page of tools.
func (c *Catalog) PageTools(cursor string, pageSize int) ([]*mcp.Tool, string, error) {
	start, end, next, err := c.page(FamilyTools, toolNames(c.tools), cursor, pageSize)
	if err != nil {
		return nil, "", err
	}
	return append([]*mcp.Tool{}, c.tools[start:end]...), next, nil
}

// PagePrompts returns one stable page of prompts.
func (c *Catalog) PagePrompts(cursor string, pageSize int) ([]*mcp.Prompt, string, error) {
	start, end, next, err := c.page(FamilyPrompts, promptNames(c.prompts), cursor, pageSize)
	if err != nil {
		return nil, "", err
	}
	return append([]*mcp.Prompt{}, c.prompts[start:end]...), next, nil
}

// PageResources returns one stable page of resources.
func (c *Catalog) PageResources(cursor string, pageSize int) ([]*mcp.Resource, string, error) {
	start, end, next, err := c.page(FamilyResources, resourceURIs(c.resources), cursor, pageSize)
	if err != nil {
		return nil, "", err
	}
	return append([]*mcp.Resource{}, c.resources[start:end]...), next, nil
}

// PageResourceTemplates returns one stable page of resource templates.
func (c *Catalog) PageResourceTemplates(cursor string, pageSize int) ([]*mcp.ResourceTemplate, string, error) {
	start, end, next, err := c.page(FamilyResourceTemplates, templateURIs(c.resourceTemplates), cursor, pageSize)
	if err != nil {
		return nil, "", err
	}
	return append([]*mcp.ResourceTemplate{}, c.resourceTemplates[start:end]...), next, nil
}
