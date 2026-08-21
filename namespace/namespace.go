// Package namespace constructs collision-safe MCP identities.
package namespace

import (
	"fmt"
	"strings"
)

const (
	maxFeatureNameLength = 128
)

// Prefix returns an explicitly configured prefix or a sanitized component name.
func Prefix(componentName, configured string) (string, error) {
	if configured != "" {
		if err := validatePart(configured); err != nil {
			return "", fmt.Errorf("invalid prefix %q: %w", configured, err)
		}
		return configured, nil
	}

	var result strings.Builder
	lastSeparator := false
	for _, r := range componentName {
		if validRune(r) {
			result.WriteString(strings.ToLower(string(r)))
			lastSeparator = false
			continue
		}
		if result.Len() > 0 && !lastSeparator {
			result.WriteByte('_')
			lastSeparator = true
		}
	}
	prefix := strings.Trim(result.String(), "_")
	if prefix == "" {
		return "", fmt.Errorf("component name %q does not contain a valid prefix character", componentName)
	}
	return prefix, nil
}

// Tool constructs an exposed tool identity, namespaced when prefix is non-empty.
func Tool(prefix, localName string) (string, error) {
	return namedFeature("tool", prefix, localName)
}

// Prompt constructs an exposed prompt identity, namespaced when prefix is non-empty.
func Prompt(prefix, localName string) (string, error) {
	return namedFeature("prompt", prefix, localName)
}

func namedFeature(family, prefix, localName string) (string, error) {
	if prefix != "" {
		if err := validatePart(prefix); err != nil {
			return "", fmt.Errorf("invalid prefix %q: %w", prefix, err)
		}
	}
	if err := validatePart(localName); err != nil {
		return "", fmt.Errorf("invalid %s name %q: %w", family, localName, err)
	}
	name := localName
	if prefix != "" {
		if !strings.HasSuffix(prefix, "_") {
			prefix += "__"
		}
		name = prefix + localName
	}
	if len(name) > maxFeatureNameLength {
		return "", fmt.Errorf("%s name %q exceeds maximum length of %d characters", family, name, maxFeatureNameLength)
	}
	return name, nil
}

func validatePart(value string) error {
	if value == "" {
		return fmt.Errorf("must not be empty")
	}
	for _, r := range value {
		if !validRune(r) {
			return fmt.Errorf("contains invalid character %q", r)
		}
	}
	return nil
}

func validRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.'
}
