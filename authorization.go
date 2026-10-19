package mmmcp

import (
	"context"
	"net/url"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
	"github.com/obot-platform/mmmcp/component"
)

// AuthorizationError reports an authorization challenge returned by an HTTP
// component.
type AuthorizationError = component.AuthorizationError

// authorizationStatus answers mmmcp_auth_status from the catalog this request already discovered.
func (c *Composite) authorizationStatus(ctx context.Context, compiled *catalog.Catalog) *mcp.CallToolResult {
	pending := compiled.PendingAuthorization()
	if len(pending) == 0 {
		return toolText("Authorization complete. The available tools have been refreshed; retry the original operation.", false)
	}
	message := "Authorization is still required for: " + strings.Join(pending, ", ") + ". After the user finishes signing in through their browser, call " + catalog.AuthorizationToolName + " again to refresh tools; do not continuously poll."
	if c.serverOptions.AuthorizationURLs == nil {
		return toolText(message, false)
	}
	links, err := c.serverOptions.AuthorizationURLs(ctx)
	if err != nil {
		// Host errors can carry internal details; keep them out of model-visible text.
		if logger := c.serverOptions.Logger; logger != nil {
			logger.ErrorContext(ctx, "building authorization links", "error", err)
		}
		return toolText("Unable to start authorization. Try again later.", true)
	}
	var shown int
	for i, link := range links {
		u, reason := validateLink(link)
		if reason != "" {
			// One malformed link must not hide the others. The URL can carry OAuth state, so log only its position.
			if logger := c.serverOptions.Logger; logger != nil {
				logger.ErrorContext(ctx, "skipping invalid authorization link", "index", i, "reason", reason)
			}
			continue
		}
		if shown == 0 {
			message += " Show these sign-in links to the user:"
		}
		shown++
		// Angle brackets keep OAuth query strings intact as clickable Markdown links.
		message += "\n" + link.Message + ": <" + u.String() + ">"
	}
	if len(links) > 0 && shown == 0 {
		return toolText(message+" No valid sign-in links are available.", true)
	}
	return toolText(message, false)
}

// validateLink returns the parsed URL, or why the link cannot be shown.
func validateLink(link AuthorizationURL) (*url.URL, string) {
	u, err := url.Parse(link.URL)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return nil, "url must be absolute http or https"
	case u.User != nil:
		return nil, "url must not contain credentials"
	case strings.ContainsFunc(link.URL, unicode.IsSpace) || strings.ContainsAny(link.URL, "<>"):
		return nil, "url contains whitespace or angle brackets"
	case !plainLabel(link.Message):
		return nil, "message contains link-like or hidden characters"
	}
	return u, ""
}

// plainLabel rejects text that Markdown could render as another link, and characters
// that could reorder, hide, or break lines around the real link.
func plainLabel(label string) bool {
	hidden := strings.ContainsFunc(label, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
	})
	linkLike := strings.ContainsAny(label, "[]<>") || strings.Contains(label, "://") || strings.Contains(strings.ToLower(label), "www.")
	return !hidden && !linkLike
}

func toolText(text string, isError bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: isError, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
