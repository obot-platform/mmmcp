package config

import (
	"time"
)

// Config is a complete composite server configuration.
type Config struct {
	Name        string
	Version     string
	Listen      string
	IdleTimeout time.Duration
	Servers     []Server
}

// Server describes one component MCP server.
type Server struct {
	Name               string
	Prefix             string
	URL                string
	Headers            map[string]string
	PassthroughHeaders []string
	Command            string
	Args               []string
	Env                map[string]string
	WorkingDirectory   string
	Timeout            time.Duration
	Tools              []ToolOverride
	DisableTools       bool
	Prompts            []PromptOverride
	Resources          []ResourceOverride
	ResourceTemplates  []ResourceTemplateOverride
}

// ToolOverride customizes a discovered tool.
type ToolOverride struct {
	Name                string
	OverrideName        string
	Description         string
	OverrideDescription string
	Enabled             bool
}

// PromptOverride customizes a discovered prompt.
type PromptOverride struct {
	Name                string
	OverrideName        string
	Description         string
	OverrideDescription string
	Enabled             bool
}

// ResourceOverride customizes a discovered resource.
type ResourceOverride struct {
	URI                 string
	OverrideURI         string
	Name                string
	OverrideName        string
	Description         string
	OverrideDescription string
	Enabled             bool
}

// ResourceTemplateOverride customizes a discovered resource template.
type ResourceTemplateOverride struct {
	URITemplate         string
	OverrideURITemplate string
	Name                string
	OverrideName        string
	Description         string
	OverrideDescription string
	Enabled             bool
}

type configDTO struct {
	Name        string        `yaml:"name"`
	Version     string        `yaml:"version"`
	Listen      string        `yaml:"listen"`
	IdleTimeout durationValue `yaml:"idleTimeout"`
	Servers     []serverDTO   `yaml:"servers"`
}

type serverDTO struct {
	Name               string                        `yaml:"name"`
	Prefix             string                        `yaml:"prefix"`
	URL                string                        `yaml:"url"`
	Headers            map[string]string             `yaml:"headers"`
	PassthroughHeaders []string                      `yaml:"passthroughHeaders"`
	Command            string                        `yaml:"command"`
	Args               []string                      `yaml:"args"`
	Env                map[string]string             `yaml:"env"`
	WorkingDirectory   string                        `yaml:"workingDirectory"`
	Timeout            durationValue                 `yaml:"timeout"`
	Tools              []toolOverrideDTO             `yaml:"tools"`
	DisableTools       bool                          `yaml:"disableTools"`
	Prompts            []promptOverrideDTO           `yaml:"prompts"`
	Resources          []resourceOverrideDTO         `yaml:"resources"`
	ResourceTemplates  []resourceTemplateOverrideDTO `yaml:"resourceTemplates"`
}

type toolOverrideDTO struct {
	Name                string `yaml:"name"`
	OverrideName        string `yaml:"overrideName"`
	Description         string `yaml:"description"`
	OverrideDescription string `yaml:"overrideDescription"`
	Enabled             *bool  `yaml:"enabled"`
}

type promptOverrideDTO struct {
	Name                string `yaml:"name"`
	OverrideName        string `yaml:"overrideName"`
	Description         string `yaml:"description"`
	OverrideDescription string `yaml:"overrideDescription"`
	Enabled             *bool  `yaml:"enabled"`
}

type resourceOverrideDTO struct {
	URI                 string `yaml:"uri"`
	OverrideURI         string `yaml:"overrideUri"`
	Name                string `yaml:"name"`
	OverrideName        string `yaml:"overrideName"`
	Description         string `yaml:"description"`
	OverrideDescription string `yaml:"overrideDescription"`
	Enabled             *bool  `yaml:"enabled"`
}

type resourceTemplateOverrideDTO struct {
	URITemplate         string `yaml:"uriTemplate"`
	OverrideURITemplate string `yaml:"overrideUriTemplate"`
	Name                string `yaml:"name"`
	OverrideName        string `yaml:"overrideName"`
	Description         string `yaml:"description"`
	OverrideDescription string `yaml:"overrideDescription"`
	Enabled             *bool  `yaml:"enabled"`
}

type durationValue struct {
	time.Duration
}

func enabledOrDefault(enabled *bool) bool {
	return enabled == nil || *enabled
}
