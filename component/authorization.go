package component

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// AuthorizationError reports an authorization challenge returned by an HTTP
// component. The challenge fields are the Bearer parameters used by MCP
// authorization flows.
type AuthorizationError struct {
	Component        string
	URL              string
	StatusCode       int
	Realm            string
	ErrorURI         string
	ResourceMetadata string
	Scope            string
	Scopes           []string
	ErrorCode        string
	ErrorDescription string
}

func (e *AuthorizationError) Error() string {
	status := http.StatusText(e.StatusCode)
	if status == "" {
		status = fmt.Sprintf("status code %d", e.StatusCode)
	} else {
		status = fmt.Sprintf("%d %s", e.StatusCode, status)
	}
	if e.ErrorCode != "" && e.ErrorDescription != "" {
		return fmt.Sprintf("component authorization failed: %s: %s (%s)", status, e.ErrorCode, e.ErrorDescription)
	}
	if e.ErrorCode != "" {
		return fmt.Sprintf("component authorization failed: %s: %s", status, e.ErrorCode)
	}
	return fmt.Sprintf("component authorization failed: %s", status)
}

// AuthorizationErrors walks joined errors without losing challenges from sibling components.
func AuthorizationErrors(err error) []*AuthorizationError {
	var result []*AuthorizationError
	var visit func(error)
	visit = func(err error) {
		if err == nil {
			return
		}

		// Match only this node: errors.AsType would select the first descendant
		// and skip sibling challenges when we return from this branch.
		if challenge, ok := err.(*AuthorizationError); ok {
			if challenge != nil && (challenge.StatusCode == http.StatusUnauthorized || challenge.StatusCode == http.StatusForbidden && challenge.ErrorCode == "insufficient_scope") {
				result = append(result, challenge)
			}

			return
		}

		switch e := err.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range e.Unwrap() {
				visit(child)
			}
		default:
			visit(errors.Unwrap(err))
		}
	}
	visit(err)

	return result
}

// AuthorizationChallenges interprets an HTTP response without consuming its body.
// Keep each Bearer challenge separate so parameters from different providers never mix.
func AuthorizationChallenges(resp *http.Response) []*AuthorizationError {
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return nil
	}

	parsed, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	var result []*AuthorizationError
	if err == nil {
		for _, challenge := range parsed {
			if !strings.EqualFold(challenge.Scheme, "bearer") || resp.StatusCode == http.StatusForbidden && challenge.Params["error"] != "insufficient_scope" {
				continue
			}

			result = append(result, &AuthorizationError{
				StatusCode:       resp.StatusCode,
				Realm:            challenge.Params["realm"],
				ErrorURI:         challenge.Params["error_uri"],
				ResourceMetadata: challenge.Params["resource_metadata"],
				Scope:            challenge.Params["scope"],
				Scopes:           strings.Fields(challenge.Params["scope"]),
				ErrorCode:        challenge.Params["error"],
				ErrorDescription: challenge.Params["error_description"],
			})
		}
	}

	if len(result) == 0 && resp.StatusCode == http.StatusUnauthorized {
		result = append(result, &AuthorizationError{StatusCode: resp.StatusCode})
	}

	return result
}
