package config

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestLoadDisableTools(t *testing.T) {
	cfg, err := Load([]byte("servers:\n  - name: gh\n    url: https://example.invalid\n    disableTools: true\n"), LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Servers[0].DisableTools {
		t.Fatal("disableTools was not loaded")
	}
}

func TestLoadStrictInterpolationAndDefaults(t *testing.T) {
	env := map[string]string{
		"TOKEN": "secret",
		"ROOT":  "/workspace",
		"MODE":  "safe",
	}
	cfg, err := Load([]byte(`
listen: 127.0.0.1:8080
idleTimeout: 30s
name: public-mcp
version: 2.3.4
servers:
  - name: local files
    url: https://example.invalid/${ROOT}
    headers:
      Authorization: Bearer ${TOKEN}
    passthroughHeaders: [X-Request-ID, X-Tenant]
    timeout: 5s
    tools:
      - name: read_file
        overrideName: read
    prompts:
      - name: explain
        enabled: false
  - name: command
    command: ${ROOT}/server
    args: ["--mode", "${MODE}", "$$HOME"]
    env:
      MODE: ${MODE}
    workingDirectory: ${ROOT}
    resources:
      - uri: file:///notes
    resourceTemplates:
      - uriTemplate: file:///{path}
`), LoadOptions{LookupEnv: func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}})
	if err != nil {
		t.Fatal(err)
	}

	want := &Config{
		Name:        "public-mcp",
		Version:     "2.3.4",
		Listen:      "127.0.0.1:8080",
		IdleTimeout: 30 * time.Second,
		Servers: []Server{
			{
				Name:               "local files",
				URL:                "https://example.invalid//workspace",
				Headers:            map[string]string{"Authorization": "Bearer secret"},
				PassthroughHeaders: []string{"X-Request-ID", "X-Tenant"},
				Args:               []string{},
				Env:                map[string]string{},
				Timeout:            5 * time.Second,
				Tools:              []ToolOverride{{Name: "read_file", OverrideName: "read", Enabled: true}},
				Prompts:            []PromptOverride{{Name: "explain", Enabled: false}},
				Resources:          []ResourceOverride{},
				ResourceTemplates:  []ResourceTemplateOverride{},
			},
			{
				Name:               "command",
				Command:            "/workspace/server",
				Args:               []string{"--mode", "safe", "$HOME"},
				Headers:            map[string]string{},
				PassthroughHeaders: []string{},
				Env:                map[string]string{"MODE": "safe"},
				WorkingDirectory:   "/workspace",
				Tools:              []ToolOverride{},
				Prompts:            []PromptOverride{},
				Resources:          []ResourceOverride{{URI: "file:///notes", Enabled: true}},
				ResourceTemplates: []ResourceTemplateOverride{{
					URITemplate: "file:///{path}", Enabled: true,
				}},
			},
		},
	}
	if diff := cmp.Diff(want, cfg); diff != "" {
		t.Fatalf("Load mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := Load([]byte("servers:\n  - name: one\n    url: https://example.invalid\n    mystery: true\n"), LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "field mystery not found") {
		t.Fatalf("Load error = %v, want unknown field", err)
	}
}

func TestLoadRejectsDSN(t *testing.T) {
	_, err := Load([]byte("dsn: local.db\nservers:\n  - name: one\n    url: https://example.invalid\n"), LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "field dsn not found") {
		t.Fatalf("Load error = %v, want dsn rejected as unknown field", err)
	}
}

func TestLoadRejectsMultipleDocuments(t *testing.T) {
	_, err := Load([]byte("servers:\n  - name: one\n    url: https://one.invalid\n---\nservers:\n  - name: two\n    url: https://two.invalid\n"), LoadOptions{})
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("Load error = %v, want multiple document error", err)
	}
}

func TestLoadSubstitutesEmptyStringForMissingEnvironmentVariable(t *testing.T) {
	cfg, err := Load([]byte("servers:\n  - name: one\n    url: https://example.invalid/${MISSING}\n    headers:\n      Authorization: Bearer ${MISSING}\n"), LoadOptions{LookupEnv: func(string) (string, bool) { return "", false }})
	if err != nil {
		t.Fatal(err)
	}
	server := cfg.Servers[0]
	if server.URL != "https://example.invalid/" || server.Headers["Authorization"] != "Bearer " {
		t.Fatalf("missing variable interpolation = URL %q, Authorization %q", server.URL, server.Headers["Authorization"])
	}
}

func TestLoadRejectsMalformedEnvironmentReferences(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"unterminated", "https://example.invalid/${MISSING", "unterminated environment reference"},
		{"empty name", "https://example.invalid/${}", "empty environment variable name"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load([]byte("servers:\n  - name: one\n    url: \""+test.value+"\"\n"), LoadOptions{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadDoesNotInterpolateIdentityFields(t *testing.T) {
	cfg, err := Load([]byte("servers:\n  - name: ${NAME}\n    prefix: ${PREFIX}\n    url: https://example.invalid\n    tools:\n      - name: ${TOOL}\n"), LoadOptions{LookupEnv: func(string) (string, bool) {
		t.Fatal("identity field unexpectedly interpolated")
		return "", false
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Servers[0]; got.Name != "${NAME}" || got.Prefix != "${PREFIX}" || got.Tools[0].Name != "${TOOL}" {
		t.Fatalf("identity fields were modified: %+v", got)
	}
}

func TestLoadValidatesTransportForms(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"neither", "servers:\n  - name: one\n", "exactly one of url or command"},
		{"both", "servers:\n  - name: one\n    url: https://example.invalid\n    command: server\n", "exactly one of url or command"},
		{"http command fields", "servers:\n  - name: one\n    url: https://example.invalid\n    args: [x]\n", "require command"},
		{"command headers", "servers:\n  - name: one\n    command: server\n    headers: {X-Test: value}\n", "requires url"},
		{"command passthrough headers", "servers:\n  - name: one\n    command: server\n    passthroughHeaders: [X-Test]\n", "requires url"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load([]byte(test.yaml), LoadOptions{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load error = %v, want %q", err, test.want)
			}
		})
	}
}
