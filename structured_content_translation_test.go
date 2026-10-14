package mmmcp

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLegacyToolOutputMovesToContent(t *testing.T) {
	tests := []struct {
		name       string
		structured any
		json       string
		content    []mcp.Content
		appendJSON bool
	}{
		{
			name:    "absent",
			content: []mcp.Content{&mcp.TextContent{Text: "authored"}},
		},
		{
			name:       "object without content",
			structured: map[string]any{"id": 1},
			json:       `{"id":1}`,
			appendJSON: true,
		},
		{
			name:       "matching text",
			structured: map[string]any{"id": 1},
			json:       `{"id":1}`,
			content:    []mcp.Content{&mcp.TextContent{Text: "\n {\"id\":1} \n"}},
		},
		{
			name:       "pretty text with reordered keys",
			structured: map[string]any{"a": 1, "b": 2},
			content:    []mcp.Content{&mcp.TextContent{Text: "{\n  \"b\": 2,\n  \"a\": 1\n}"}},
		},
		{
			name:       "JSON followed by text",
			structured: map[string]any{"id": 1},
			json:       `{"id":1}`,
			content:    []mcp.Content{&mcp.TextContent{Text: `{"id":1} extra`}},
			appendJSON: true,
		},
		{
			name:       "array with media",
			structured: []int{1, 2},
			json:       `[1,2]`,
			content:    []mcp.Content{&mcp.ImageContent{MIMEType: "image/png", Data: []byte{1}}},
			appendJSON: true,
		},
		{
			name:       "explicit null",
			structured: json.RawMessage("null"),
			json:       `null`,
			appendJSON: true,
		},
		{
			name:       "large number",
			structured: json.RawMessage(`{"id":9007199254740993}`),
			json:       `{"id":9007199254740993}`,
			content:    []mcp.Content{&mcp.TextContent{Text: `{"id":9007199254740992}`}},
			appendJSON: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &mcp.ServerRequest[*mcp.CallToolParams]{Params: &mcp.CallToolParams{Meta: mcp.Meta{mcp.MetaKeyProtocolVersion: "2025-11-25"}}}
			original := &mcp.CallToolResult{Content: test.content, StructuredContent: test.structured}
			before := string(mustJSON(t, original))
			result, err := normalizeCallToolResult(request, original, currentProtocolVersion)
			if err != nil {
				t.Fatal(err)
			}
			if _, present := marshalWireResult(t, result)["structuredContent"]; present {
				t.Fatal("legacy structuredContent was retained")
			}
			wantLength := len(original.Content)
			if test.appendJSON {
				wantLength++
			}
			if len(result.Content) != wantLength {
				t.Fatalf("content length = %d, want %d", len(result.Content), wantLength)
			}
			if len(original.Content) > 0 && !reflect.DeepEqual(result.Content[:len(original.Content)], original.Content) {
				t.Fatal("authored content was changed")
			}
			if test.appendJSON {
				if text, ok := result.Content[wantLength-1].(*mcp.TextContent); !ok || text.Text != test.json {
					t.Fatalf("legacy JSON data missing: %+v", result.Content)
				}
			}
			if string(mustJSON(t, original)) != before {
				t.Fatal("shared upstream result was mutated")
			}
			if test.json == "null" {
				request.Params.Meta[mcp.MetaKeyProtocolVersion] = currentProtocolVersion
				modern, err := normalizeCallToolResult(request, original, currentProtocolVersion)
				if err != nil {
					t.Fatal(err)
				}
				if string(marshalWireResult(t, modern)["structuredContent"]) != "null" {
					t.Fatal("modern explicit null was lost")
				}
			}
		})
	}
}
