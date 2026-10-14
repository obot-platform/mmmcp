package mmmcp

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
)

type operationResult interface {
	NeedsInput() bool
}

// omitModernToolOutputSchemas omits output schemas from 2026-07-28 upstreams for
// 2025-11-25 frontends.
// Copy affected definitions so omission does not mutate the cached catalog.
func omitModernToolOutputSchemas(request mcp.Request, tools []*mcp.Tool, compiled *catalog.Catalog) []*mcp.Tool {
	var projected []*mcp.Tool
	for i, tool := range tools {
		definition := omitModernToolOutputSchema(request, tool, compiled)
		if definition == tool {
			continue
		}
		if projected == nil {
			projected = slices.Clone(tools)
		}
		projected[i] = definition
	}
	if projected == nil {
		return tools
	}
	return projected
}

func omitModernToolOutputSchema(request mcp.Request, tool *mcp.Tool, compiled *catalog.Catalog) *mcp.Tool {
	if tool == nil || tool.OutputSchema == nil {
		return tool
	}
	route, ok := compiled.RouteTool(tool.Name)
	if !ok || !shouldOmitModernToolOutputSchema(request, route.ProtocolVersion) {
		return tool
	}
	// List definitions share cached tools across frontend versions.
	// Copy the struct before clearing its field; nested schemas remain unchanged.
	clone := *tool
	clone.OutputSchema = nil
	return &clone
}

func normalizeCallToolResult(request mcp.Request, result *mcp.CallToolResult) (*mcp.CallToolResult, error) {
	return normalizeOperationResult(request, result, func() *mcp.CallToolResult { return new(mcp.CallToolResult) }, false)
}

func normalizeGetPromptResult(request mcp.Request, result *mcp.GetPromptResult) (*mcp.GetPromptResult, error) {
	return normalizeOperationResult(request, result, func() *mcp.GetPromptResult { return new(mcp.GetPromptResult) }, false)
}

func normalizeReadResourceResult(request mcp.Request, result *mcp.ReadResourceResult) (*mcp.ReadResourceResult, error) {
	return normalizeOperationResult(request, result, func() *mcp.ReadResourceResult { return new(mcp.ReadResourceResult) }, true)
}

func normalizeOperationResult[T operationResult](request mcp.Request, result T, create func() T, cacheable bool) (T, error) {
	var zero T
	if any(result) == nil {
		return result, nil
	}
	current := frontendUsesCurrentProtocol(request)
	if !current && result.NeedsInput() {
		return zero, &jsonrpc.Error{
			Code:    jsonrpc.CodeInternalError,
			Message: "downstream returned an input-required result that cannot be represented for a legacy frontend",
		}
	}

	data, err := json.Marshal(result)
	if err != nil {
		return zero, fmt.Errorf("marshal downstream operation result: %w", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		return zero, fmt.Errorf("decode downstream operation result: %w", err)
	}
	if current {
		resultType := "complete"
		if result.NeedsInput() {
			resultType = "input_required"
		}
		wire["resultType"], _ = json.Marshal(resultType)
		if cacheable {
			if scope, ok := wire["cacheScope"]; !ok || string(scope) == `""` || string(scope) == "null" {
				wire["cacheScope"] = json.RawMessage(`"public"`)
			}
			if _, ok := wire["ttlMs"]; !ok {
				wire["ttlMs"] = json.RawMessage("0")
			}
		}
	} else {
		delete(wire, "resultType")
	}
	stripServerInfo(wire)

	data, err = json.Marshal(wire)
	if err != nil {
		return zero, fmt.Errorf("marshal normalized operation result: %w", err)
	}
	normalized := create()
	if err := json.Unmarshal(data, normalized); err != nil {
		return zero, fmt.Errorf("decode normalized operation result: %w", err)
	}
	return normalized, nil
}

// shouldOmitModernToolOutputSchema limits this compatibility policy to the exact known
// upstream/frontend protocol pair, independently of the SDK's current version.
func shouldOmitModernToolOutputSchema(request mcp.Request, upstreamProtocolVersion string) bool {
	// The SDK accessor reads modern request metadata or the legacy handshake.
	frontend, ok := request.(interface{ ProtocolVersion() string })
	return upstreamProtocolVersion == "2026-07-28" && ok && frontend.ProtocolVersion() == "2025-11-25"
}

func frontendUsesCurrentProtocol(request mcp.Request) bool {
	if request == nil || request.GetParams() == nil {
		return false
	}
	version, _ := request.GetParams().GetMeta()[mcp.MetaKeyProtocolVersion].(string)
	return version >= currentProtocolVersion
}

func stripServerInfo(wire map[string]json.RawMessage) {
	raw, ok := wire["_meta"]
	if !ok {
		return
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(raw, &meta) != nil {
		return
	}
	delete(meta, mcp.MetaKeyServerInfo)
	if len(meta) == 0 {
		delete(wire, "_meta")
		return
	}
	wire["_meta"], _ = json.Marshal(meta)
}
