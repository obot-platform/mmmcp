package catalog

import (
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// AuthorizationToolName is the local tool clients call after completing component OAuth.
	AuthorizationToolName = "mmmcp_auth_status"
)

// PendingAuthorization lists components omitted until the user completes authorization.
func (c *Catalog) PendingAuthorization() []string {
	return slices.Clone(c.pendingAuthorization)
}

func authorizationTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        AuthorizationToolName,
		Description: "Configure and authenticate all MCP components before using their tools. Call this tool to request clickable setup links. After the user finishes signing in, call it again to refresh available tools without restarting the session. Opening a link is not completed authorization. Do not continuously poll.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
	}
}
