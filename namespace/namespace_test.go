package namespace

import (
	"strings"
	"testing"
)

func TestPrefix(t *testing.T) {
	tests := []struct {
		name       string
		component  string
		configured string
		want       string
	}{
		{"configured", "ignored", "gh", "gh"},
		{"sanitize spaces", "local files", "", "local_files"},
		{"collapse separators", " local / files ", "", "local_files"},
		{"lowercase server name", "GitHub.v2-api", "", "github.v2-api"},
		{"preserve explicit case", "ignored", "GitHub", "GitHub"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Prefix(test.component, test.configured)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("Prefix() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPrefixRejectsInvalidExplicitPrefix(t *testing.T) {
	if _, err := Prefix("component", "not valid"); err == nil {
		t.Fatal("Prefix accepted invalid explicit prefix")
	}
	if _, err := Prefix("!!!", ""); err == nil {
		t.Fatal("Prefix accepted component with empty sanitized prefix")
	}
}

func TestTool(t *testing.T) {
	got, err := Tool("github", "search.code")
	if err != nil {
		t.Fatal(err)
	}
	if got != "github__search.code" {
		t.Fatalf("Tool() = %q", got)
	}
}

func TestToolValidatesSyntaxAndLength(t *testing.T) {
	if _, err := Tool("good", "bad/name"); err == nil {
		t.Fatal("Tool accepted invalid character")
	}
	if _, err := Tool(strings.Repeat("p", 64), strings.Repeat("t", 64)); err == nil {
		t.Fatal("Tool accepted name over 128 bytes")
	}
}

func TestPromptAndResourceIdentities(t *testing.T) {
	prompt, err := Prompt("docs", "summarize")
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "docs__summarize" {
		t.Fatalf("Prompt() = %q", prompt)
	}
	resource, err := Resource("docs", "file:///notes")
	if err != nil {
		t.Fatal(err)
	}
	if resource != "file://docs//notes" {
		t.Fatalf("Resource() = %q", resource)
	}
	template, err := ResourceTemplate("docs", "file:///{path}")
	if err != nil {
		t.Fatal(err)
	}
	if template != "file://docs//{path}" {
		t.Fatalf("ResourceTemplate() = %q", template)
	}
}

func TestFeatureIdentitiesWithoutPrefix(t *testing.T) {
	tool, err := Tool("", "search")
	if err != nil || tool != "search" {
		t.Fatalf("Tool() = %q, %v", tool, err)
	}
	prompt, err := Prompt("", "summarize")
	if err != nil || prompt != "summarize" {
		t.Fatalf("Prompt() = %q, %v", prompt, err)
	}
	resource, err := Resource("", "file:///notes")
	if err != nil || resource != "file:///notes" {
		t.Fatalf("Resource() = %q, %v", resource, err)
	}
	template, err := ResourceTemplate("", "file:///{path}")
	if err != nil || template != "file:///{path}" {
		t.Fatalf("ResourceTemplate() = %q, %v", template, err)
	}
}

func TestResourceIdentitiesRejectMalformedValues(t *testing.T) {
	if _, err := Resource("docs", "relative/path"); err == nil {
		t.Fatal("Resource accepted a relative URI")
	}
	if _, err := ResourceTemplate("docs", "relative/{path}"); err == nil {
		t.Fatal("ResourceTemplate accepted a relative template")
	}
	if _, err := ResourceTemplate("docs", "file:///{path"); err == nil {
		t.Fatal("ResourceTemplate accepted malformed syntax")
	}
}

func TestUIResourcesKeepUIScheme(t *testing.T) {
	resource, err := Resource("images_", "ui://images/review.html")
	if err != nil || resource != "ui://images_/images/review.html" {
		t.Fatalf("Resource() = %q, %v", resource, err)
	}
	template, err := ResourceTemplate("docs", "ui://viewer/{id}")
	if err != nil || template != "ui://docs/viewer/{id}" {
		t.Fatalf("ResourceTemplate() = %q, %v", template, err)
	}
	opaque, err := Resource("docs", "urn:example:notes")
	if err != nil || opaque != "urn:docs/example:notes" {
		t.Fatalf("Resource() for opaque URI = %q, %v", opaque, err)
	}
	unprefixed, err := Resource("", "ui://images/review.html")
	if err != nil || unprefixed != "ui://images/review.html" {
		t.Fatalf("Resource() without prefix = %q, %v", unprefixed, err)
	}
}
