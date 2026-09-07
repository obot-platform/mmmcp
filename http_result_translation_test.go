package mmmcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp"
	"github.com/obot-platform/mmmcp/config"
	"github.com/obot-platform/mmmcp/testserver"
)

type operationWireCase struct {
	method string
	params map[string]any
}

func TestEmptyListResultsAreArrays(t *testing.T) {
	composite, err := mmmcp.New(t.Context(), &config.Config{}, mmmcp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = composite.Close() })
	frontend := httptest.NewServer(composite.HTTPHandler())
	t.Cleanup(frontend.Close)
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			var sessionID string
			var meta map[string]any
			if version == "2025-11-25" {
				sessionID = initializeLegacyRawSession(t, frontend)
			} else {
				meta = map[string]any{mcp.MetaKeyProtocolVersion: version, mcp.MetaKeyClientCapabilities: map[string]any{}}
			}
			for method, field := range map[string]string{
				"tools/list":               "tools",
				"prompts/list":             "prompts",
				"resources/list":           "resources",
				"resources/templates/list": "resourceTemplates",
			} {
				result := rawOperationCall(t, frontend, method, map[string]any{}, meta, version, sessionID)
				if got := string(result[field]); got != "[]" {
					t.Errorf("%s: %s = %s, want []", method, field, got)
				}
			}
		})
	}
}

func TestCurrentFrontendOperationResultsFromStatefulHTTPDownstream(t *testing.T) {
	frontend := operationTranslationFrontend(t, true)
	meta := map[string]any{
		mcp.MetaKeyProtocolVersion:    "2026-07-28",
		mcp.MetaKeyClientCapabilities: map[string]any{},
	}
	for _, operation := range operationWireCases() {
		t.Run(operation.method, func(t *testing.T) {
			result := rawOperationCall(t, frontend, operation.method, operation.params, meta, "2026-07-28", "")
			if got := rawString(t, result["resultType"]); got != "complete" {
				t.Fatalf("resultType = %q, want complete; result = %s", got, mustMarshal(t, result))
			}
			if operation.method == "resources/read" {
				if got := rawString(t, result["cacheScope"]); got != "public" {
					t.Fatalf("cacheScope = %q, want public; result = %s", got, mustMarshal(t, result))
				}
				if got := string(result["ttlMs"]); got != "0" {
					t.Fatalf("ttlMs = %s, want 0; result = %s", got, mustMarshal(t, result))
				}
			}
		})
	}
}

func TestLegacyFrontendOperationResultsFromStatelessHTTPDownstream(t *testing.T) {
	frontend := operationTranslationFrontend(t, false)
	sessionID := initializeLegacyRawSession(t, frontend)
	for _, operation := range operationWireCases() {
		t.Run(operation.method, func(t *testing.T) {
			result := rawOperationCall(t, frontend, operation.method, operation.params, nil, "2025-11-25", sessionID)
			if _, ok := result["resultType"]; ok {
				t.Fatalf("legacy result contains resultType: %s", mustMarshal(t, result))
			}
		})
	}
}

func operationWireCases() []operationWireCase {
	return []operationWireCase{
		{method: "tools/call", params: map[string]any{"name": "tool", "arguments": map[string]any{}}},
		{method: "prompts/get", params: map[string]any{"name": "prompt", "arguments": map[string]string{}}},
		{method: "resources/read", params: map[string]any{"uri": "file:///resource"}},
	}
}

func operationTranslationFrontend(t *testing.T, statefulDownstream bool) *httptest.Server {
	t.Helper()
	fixture := testserver.New(t, testserver.Options{
		Stateful: statefulDownstream,
		Tools: []testserver.Tool{{
			Definition: &mcp.Tool{Name: "tool", InputSchema: map[string]any{"type": "object"}},
			Handler: func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
			},
		}},
		Prompts: []testserver.Prompt{{
			Definition: &mcp.Prompt{Name: "prompt"},
			Handler: func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{}}, nil
			},
		}},
		Resources: []testserver.Resource{{
			Definition: &mcp.Resource{Name: "resource", URI: "file:///resource"},
			Handler: func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "ok"}}}, nil
			},
		}},
	})
	composite, err := mmmcp.New(t.Context(), &config.Config{Servers: []config.Server{{Name: "fixture", URL: fixture.URL}}}, mmmcp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = composite.Close() })
	frontend := httptest.NewServer(composite.HTTPHandler())
	t.Cleanup(frontend.Close)
	return frontend
}

func initializeLegacyRawSession(t *testing.T, frontend *httptest.Server) string {
	t.Helper()
	params := map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "raw-legacy-test", "version": "1"},
	}
	response := rawMCPPost(t, frontend, "initialize", params, "2025-11-25", "")
	sessionID := response.Header.Get("Mcp-Session-Id")
	response.Body.Close()
	if sessionID == "" {
		t.Fatal("legacy initialize response omitted Mcp-Session-Id")
	}
	return sessionID
}

func rawOperationCall(t *testing.T, frontend *httptest.Server, method string, params map[string]any, meta map[string]any, version, sessionID string) map[string]json.RawMessage {
	t.Helper()
	if meta != nil {
		params = cloneParams(params)
		params["_meta"] = meta
	}
	response := rawMCPPost(t, frontend, method, params, version, sessionID)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s status = %d, body = %s", method, response.StatusCode, body)
	}
	var message struct {
		Result map[string]json.RawMessage `json:"result"`
		Error  json.RawMessage            `json:"error"`
	}
	if err := json.Unmarshal(body, &message); err != nil {
		t.Fatalf("%s response decode: %v; body = %s", method, err, body)
	}
	if len(message.Error) != 0 && string(message.Error) != "null" {
		t.Fatalf("%s protocol error: %s", method, message.Error)
	}
	return message.Result
}

func rawMCPPost(t *testing.T, frontend *httptest.Server, method string, params map[string]any, version, sessionID string) *http.Response {
	t.Helper()
	body := mustMarshal(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	request, err := http.NewRequest(http.MethodPost, frontend.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Mcp-Protocol-Version", version)
	request.Header.Set("Mcp-Method", method)
	if version >= "2026-07-28" {
		if name, _ := params["name"].(string); name != "" {
			request.Header.Set("Mcp-Name", name)
		} else if uri, _ := params["uri"].(string); uri != "" {
			request.Header.Set("Mcp-Name", uri)
		}
	}
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}
	response, err := frontend.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func cloneParams(params map[string]any) map[string]any {
	clone := make(map[string]any, len(params)+1)
	maps.Copy(clone, params)
	return clone
}

func rawString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
