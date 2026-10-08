package namespace

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/yosida95/uritemplate/v3"
)

// Resource constructs an exposed resource URI, namespaced when prefix is non-empty.
func Resource(prefix, original string) (string, error) {
	if prefix != "" {
		if err := validatePart(prefix); err != nil {
			return "", fmt.Errorf("invalid prefix %q: %w", prefix, err)
		}
	}
	parsed, err := url.Parse(original)
	if err != nil || parsed.Scheme == "" {
		return "", fmt.Errorf("invalid resource URI %q", original)
	}
	if prefix == "" {
		return original, nil
	}
	return compose(prefix, original), nil
}

// ResourceTemplate constructs an exposed URI template, namespaced when prefix is non-empty.
func ResourceTemplate(prefix, original string) (string, error) {
	if prefix != "" {
		if err := validatePart(prefix); err != nil {
			return "", fmt.Errorf("invalid prefix %q: %w", prefix, err)
		}
	}
	if _, err := uritemplate.New(original); err != nil {
		return "", fmt.Errorf("invalid resource URI template %q: %w", original, err)
	}
	if !hasTemplateScheme(original) {
		return "", fmt.Errorf("invalid resource URI template %q: missing scheme", original)
	}
	if prefix == "" {
		return original, nil
	}
	return compose(prefix, original), nil
}

// compose namespaces a URI or URI template by inserting the prefix as the first
// segment after the scheme, so the original scheme is kept (MCP Apps hosts only
// render ui:// resources). The rest of the original is kept verbatim, which keeps
// the mapping one-to-one; prefixes never contain '/' or ':'.
//
//	ui://viewer/card.html -> ui://<prefix>/viewer/card.html
//	file:///notes         -> file://<prefix>//notes
//	urn:example:a         -> urn:<prefix>/example:a
func compose(prefix, original string) string {
	scheme, rest, _ := strings.Cut(original, ":")
	if path, ok := strings.CutPrefix(rest, "//"); ok {
		return scheme + "://" + prefix + "/" + path
	}
	return scheme + ":" + prefix + "/" + rest
}

func hasTemplateScheme(value string) bool {
	colon := strings.IndexByte(value, ':')
	if colon <= 0 || strings.Contains(value[:colon], "{") {
		return false
	}
	for i, r := range value[:colon] {
		if i == 0 && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
		if i > 0 && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '+' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}
