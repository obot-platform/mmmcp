package mmmcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
	"github.com/obot-platform/mmmcp/config"
)

type projectionDiscoverer struct {
	features *component.Features
}

func (d projectionDiscoverer) Discover(context.Context, config.Server) (*component.Features, error) {
	return d.features, nil
}

func TestNormalizeOperationResultsForCurrentFrontendWire(t *testing.T) {
	meta := mcp.Meta{mcp.MetaKeyProtocolVersion: currentProtocolVersion}
	tests := []struct {
		name      string
		normalize func() (any, error)
		cacheable bool
	}{
		{
			name: "tools/call",
			normalize: func() (any, error) {
				request := &mcp.ServerRequest[*mcp.CallToolParams]{Params: &mcp.CallToolParams{Meta: meta}}
				return normalizeCallToolResult(request, &mcp.CallToolResult{Meta: downstreamServerMeta(), Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, currentProtocolVersion)
			},
		},
		{
			name: "prompts/get",
			normalize: func() (any, error) {
				request := &mcp.ServerRequest[*mcp.GetPromptParams]{Params: &mcp.GetPromptParams{Meta: meta}}
				return normalizeGetPromptResult(request, &mcp.GetPromptResult{Meta: downstreamServerMeta(), Messages: []*mcp.PromptMessage{}})
			},
		},
		{
			name:      "resources/read",
			cacheable: true,
			normalize: func() (any, error) {
				request := &mcp.ServerRequest[*mcp.ReadResourceParams]{Params: &mcp.ReadResourceParams{Meta: meta}}
				return normalizeReadResourceResult(request, &mcp.ReadResourceResult{Meta: downstreamServerMeta(), Contents: []*mcp.ResourceContents{}})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := test.normalize()
			if err != nil {
				t.Fatal(err)
			}
			wire := marshalWireResult(t, result)
			if got := wireString(t, wire, "resultType"); got != "complete" {
				t.Fatalf("resultType = %q, want complete; wire = %s", got, mustJSON(t, result))
			}
			assertNoDownstreamServerInfo(t, wire)
			if test.cacheable {
				if got := wireString(t, wire, "cacheScope"); got != "public" {
					t.Fatalf("cacheScope = %q, want public", got)
				}
				if got := string(wire["ttlMs"]); got != "0" {
					t.Fatalf("ttlMs = %s, want 0", got)
				}
			}
		})
	}
}

func TestNormalizeOperationResultsForLegacyFrontendWire(t *testing.T) {
	tests := []struct {
		name      string
		normalize func() (any, error)
	}{
		{
			name: "tools/call",
			normalize: func() (any, error) {
				var downstream mcp.CallToolResult
				unmarshalCurrentResult(t, &downstream)
				return normalizeCallToolResult(&mcp.ServerRequest[*mcp.CallToolParams]{Params: &mcp.CallToolParams{}}, &downstream, currentProtocolVersion)
			},
		},
		{
			name: "prompts/get",
			normalize: func() (any, error) {
				var downstream mcp.GetPromptResult
				unmarshalCurrentResult(t, &downstream)
				return normalizeGetPromptResult(&mcp.ServerRequest[*mcp.GetPromptParams]{Params: &mcp.GetPromptParams{}}, &downstream)
			},
		},
		{
			name: "resources/read",
			normalize: func() (any, error) {
				var downstream mcp.ReadResourceResult
				unmarshalCurrentResult(t, &downstream)
				return normalizeReadResourceResult(&mcp.ServerRequest[*mcp.ReadResourceParams]{Params: &mcp.ReadResourceParams{}}, &downstream)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := test.normalize()
			if err != nil {
				t.Fatal(err)
			}
			wire := marshalWireResult(t, result)
			if _, ok := wire["resultType"]; ok {
				t.Fatalf("legacy wire contains resultType: %s", mustJSON(t, result))
			}
			assertNoDownstreamServerInfo(t, wire)
		})
	}
}

func downstreamServerMeta() mcp.Meta {
	return mcp.Meta{mcp.MetaKeyServerInfo: map[string]any{"name": "downstream", "version": "1"}, "kept": true}
}

func unmarshalCurrentResult(t *testing.T, result any) {
	t.Helper()
	if err := json.Unmarshal([]byte(`{"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"downstream","version":"1"},"kept":true},"resultType":"complete","content":[],"messages":[],"contents":[],"cacheScope":"public","ttlMs":0}`), result); err != nil {
		t.Fatal(err)
	}
}

func marshalWireResult(t *testing.T, result any) map[string]json.RawMessage {
	t.Helper()
	data := mustJSON(t, result)
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	return wire
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func wireString(t *testing.T, wire map[string]json.RawMessage, key string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(wire[key], &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertNoDownstreamServerInfo(t *testing.T, wire map[string]json.RawMessage) {
	t.Helper()
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(wire["_meta"], &meta); err != nil {
		t.Fatal(err)
	}
	if _, ok := meta[mcp.MetaKeyServerInfo]; ok {
		t.Fatalf("downstream server info was forwarded: %s", wire["_meta"])
	}
	if _, ok := meta["kept"]; !ok {
		t.Fatalf("application metadata was discarded: %s", wire["_meta"])
	}
}

func TestOutputSchemaOmissionPreservesCatalog(t *testing.T) {
	schemas := []any{
		nil,
		map[string]any{"type": "object"},
		map[string]any{"oneOf": []any{map[string]any{"type": "null"}, map[string]any{"type": "object"}}},
		true,
		false,
	}
	tools := make([]*mcp.Tool, len(schemas))
	for i, schema := range schemas {
		tools[i] = &mcp.Tool{
			Name:         "tool" + strconv.Itoa(i),
			Meta:         mcp.Meta{"custom": true},
			InputSchema:  map[string]any{"type": "object"},
			OutputSchema: schema,
		}
	}
	compiled, err := catalog.Compile(t.Context(), &config.Config{Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}, projectionDiscoverer{features: &component.Features{ProtocolVersion: currentProtocolVersion, Tools: tools}})
	if err != nil {
		t.Fatal(err)
	}
	tools = compiled.Tools()
	before := string(mustJSON(t, tools))
	legacy := &mcp.ListToolsRequest{Params: &mcp.ListToolsParams{Meta: mcp.Meta{mcp.MetaKeyProtocolVersion: "2025-11-25"}}}
	modern := &mcp.ListToolsRequest{Params: &mcp.ListToolsParams{Meta: mcp.Meta{mcp.MetaKeyProtocolVersion: currentProtocolVersion}}}
	for _, request := range []mcp.Request{legacy, modern} {
		projected := omitModernToolOutputSchemas(request, tools, compiled)
		if len(projected) != len(tools) {
			t.Fatal("projection dropped tools")
		}
		for i, tool := range projected {
			if request == modern {
				if tool != tools[i] {
					t.Fatal("modern schema definition was changed")
				}
				continue
			}
			wire := marshalWireResult(t, tool)
			if _, present := wire["outputSchema"]; present {
				t.Fatalf("legacy tool still advertises outputSchema: %s", mustJSON(t, tool))
			}
			expected := *tools[i]
			expected.OutputSchema = nil
			if !reflect.DeepEqual(tool, &expected) {
				t.Fatal("projection changed unrelated tool fields")
			}
		}
		if string(mustJSON(t, tools)) != before {
			t.Fatal("cached catalog was mutated")
		}
	}
}

func TestToolOutputTranslationRequiresExactProtocolPair(t *testing.T) {
	tests := []struct {
		name     string
		upstream string
		frontend string
		omit     bool
	}{
		{
			name:     "target pair",
			upstream: "2026-07-28",
			frontend: "2025-11-25",
			omit:     true,
		},
		{
			name:     "legacy upstream",
			upstream: "2025-11-25",
			frontend: "2025-11-25",
		},
		{
			name:     "older upstream",
			upstream: "2025-06-18",
			frontend: "2025-11-25",
		},
		{
			name:     "future upstream",
			upstream: "2027-01-01",
			frontend: "2025-11-25",
		},
		{
			name:     "unknown upstream",
			frontend: "2025-11-25",
		},
		{
			name:     "modern frontend",
			upstream: "2026-07-28",
			frontend: "2026-07-28",
		},
		{
			name:     "other legacy frontend",
			upstream: "2026-07-28",
			frontend: "2025-06-18",
		},
		{
			name:     "unknown frontend",
			upstream: "2026-07-28",
		},
		{
			name:     "future frontend",
			upstream: "2026-07-28",
			frontend: "2027-01-01",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool := &mcp.Tool{Name: "object", InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object"}}
			compiled, err := catalog.Compile(t.Context(), &config.Config{Servers: []config.Server{{Name: "fixture", URL: "https://example.invalid"}}}, projectionDiscoverer{features: &component.Features{ProtocolVersion: test.upstream, Tools: []*mcp.Tool{tool}}})
			if err != nil {
				t.Fatal(err)
			}
			request := &mcp.ServerRequest[*mcp.CallToolParams]{Params: &mcp.CallToolParams{Meta: mcp.Meta{mcp.MetaKeyProtocolVersion: test.frontend}}}
			tools := omitModernToolOutputSchemas(request, compiled.Tools(), compiled)
			if (tools[0].OutputSchema == nil) != test.omit {
				t.Fatalf("outputSchema = %#v, want omitted=%t", tools[0].OutputSchema, test.omit)
			}
			result, err := normalizeCallToolResult(request, &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: "[1,2]"}},
				StructuredContent: []int{1, 2},
			}, test.upstream)
			if err != nil {
				t.Fatal(err)
			}
			if (result.StructuredContent == nil) != test.omit || len(result.Content) != 1 {
				t.Fatalf("unexpected array translation for protocol pair: %+v", result)
			}
		})
	}
}

func TestLegacyStructuredToolContent(t *testing.T) {
	for _, test := range []struct {
		name           string
		value          any
		text           string
		wantStructured string
		wantExtraText  string
	}{
		{
			name:           "object is retained",
			value:          map[string]any{"ok": true},
			text:           "ok",
			wantStructured: `{"ok":true}`,
		},
		{
			name:  "formatted array is not duplicated",
			value: json.RawMessage(`[{"a":1,"b":2}]`),
			text:  "[\n  {\"b\": 2, \"a\": 1}\n]",
		},
		{
			name:          "different large integers",
			value:         json.RawMessage(`[9007199254740993]`),
			text:          "[9007199254740992]",
			wantExtraText: "[9007199254740993]",
		},
		{
			name:          "JSON with trailing text",
			value:         []int{1},
			text:          "[1] extra",
			wantExtraText: "[1]",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := &mcp.ServerRequest[*mcp.CallToolParams]{Params: &mcp.CallToolParams{Meta: mcp.Meta{mcp.MetaKeyProtocolVersion: "2025-11-25"}}}
			original := &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: test.text}},
				StructuredContent: test.value,
			}
			before := string(mustJSON(t, original))
			result, err := normalizeCallToolResult(request, original, currentProtocolVersion)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(marshalWireResult(t, result)["structuredContent"]); got != test.wantStructured {
				t.Fatalf("structuredContent = %s, want %s", got, test.wantStructured)
			}
			wantContent := append([]mcp.Content(nil), original.Content...)
			if test.wantExtraText != "" {
				wantContent = append(wantContent, &mcp.TextContent{Text: test.wantExtraText})
			}
			if !reflect.DeepEqual(result.Content, wantContent) {
				t.Fatalf("content = %+v, want %+v", result.Content, wantContent)
			}
			if string(mustJSON(t, original)) != before {
				t.Fatal("upstream result was mutated")
			}
		})
	}
}
