package http

import (
	"context"
	"reflect"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/obot-platform/mmmcp/config"
	"golang.org/x/oauth2"
)

// OAuthHandlerProvider returns the OAuth handler for a configured HTTP
// component. Implementations may load component-specific credentials and
// tokens from durable storage before constructing the handler.
type OAuthHandlerProvider interface {
	OAuthHandler(context.Context, config.Server) (auth.OAuthHandler, error)
}

// OAuthHandlerProviderFunc adapts a function into an OAuthHandlerProvider.
type OAuthHandlerProviderFunc func(context.Context, config.Server) (auth.OAuthHandler, error)

// TokenStore persists an OAuth token. The token includes its access token,
// refresh token, type, expiry, and provider-specific extra fields.
type TokenStore interface {
	StoreToken(context.Context, *oauth2.Token) error
}

// TokenStoreFunc adapts a function into a TokenStore.
type TokenStoreFunc func(context.Context, *oauth2.Token) error

type persistentTokenSource struct {
	ctx    context.Context
	source oauth2.TokenSource
	store  TokenStore

	mu   sync.Mutex
	last *oauth2.Token
}

// OAuthHandler implements OAuthHandlerProvider.
func (f OAuthHandlerProviderFunc) OAuthHandler(ctx context.Context, server config.Server) (auth.OAuthHandler, error) {
	return f(ctx, server)
}

// StoreToken implements TokenStore.
func (f TokenStoreFunc) StoreToken(ctx context.Context, token *oauth2.Token) error {
	return f(ctx, token)
}

// NewPersistentTokenSource wraps source so that each new token it returns is
// written to store before it is returned to the caller. A storage failure is
// returned as a token error, preventing a refreshed token from being used
// without first being persisted.
//
// This function is intended for auth.AuthorizationCodeHandlerConfig's
// NewTokenSource hook as well as InitialTokenSource. The context should outlive
// individual MCP requests; the SDK supplies such a context to NewTokenSource.
func NewPersistentTokenSource(ctx context.Context, source oauth2.TokenSource, store TokenStore) oauth2.TokenSource {
	if source == nil || store == nil {
		return source
	}
	return &persistentTokenSource{ctx: ctx, source: source, store: store}
}
func (s *persistentTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	token, err := s.source.Token()
	if err != nil || token == nil {
		return token, err
	}
	if reflect.DeepEqual(s.last, token) {
		return token, nil
	}
	if err := s.store.StoreToken(s.ctx, token); err != nil {
		return nil, err
	}
	clone := *token
	s.last = &clone
	return token, nil
}
