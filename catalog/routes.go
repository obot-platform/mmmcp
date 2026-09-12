package catalog

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/config"
	"github.com/yosida95/uritemplate/v3"
)

// ToolRoute maps an exposed tool identity back to its component identity.
type ToolRoute struct {
	Component config.Server
	Prefix    string
	Tool      *mcp.Tool
}

// PromptRoute maps an exposed prompt identity back to its component identity.
type PromptRoute struct {
	Component    config.Server
	Prefix       string
	OriginalName string
}

// ResourceRoute maps an exposed resource URI back to its component URI.
type ResourceRoute struct {
	Component    config.Server
	Prefix       string
	OriginalURI  string
	CompositeURI string
}

// ResourceTemplateRoute retains both template directions without guessing from strings.
type ResourceTemplateRoute struct {
	Component         config.Server
	Prefix            string
	OriginalTemplate  string
	CompositeTemplate string
	exposed           *uritemplate.Template
	original          *uritemplate.Template
}

func (r ResourceTemplateRoute) toOriginal(uri string) (string, bool) {
	values := r.exposed.Match(uri)
	if values == nil {
		return "", false
	}
	result, err := r.original.Expand(values)
	return result, err == nil
}

func (r ResourceTemplateRoute) toComposite(uri string) (string, bool) {
	values := r.original.Match(uri)
	if values == nil {
		return "", false
	}
	result, err := r.exposed.Expand(values)
	return result, err == nil
}
