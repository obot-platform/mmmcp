package http_test

import (
	"context"
	"errors"
	"testing"
	"time"

	componenthttp "github.com/obot-platform/mmmcp/component/http"
	"golang.org/x/oauth2"
)

type tokenSequence struct {
	tokens []*oauth2.Token
	next   int
}

type recordingTokenStore struct {
	tokens []*oauth2.Token
}

func TestPersistentTokenSourceStoresInitialAndRefreshedTokens(t *testing.T) {
	initial := (&oauth2.Token{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		TokenType:    "Bearer",
		Expiry:       time.Unix(100, 0),
	}).WithExtra(map[string]any{"scope": "tools.read"})
	refreshed := (&oauth2.Token{
		AccessToken:  "access-2",
		RefreshToken: "refresh-2",
		TokenType:    "Bearer",
		Expiry:       time.Unix(200, 0),
	}).WithExtra(map[string]any{"scope": "tools.read tools.write"})
	source := &tokenSequence{tokens: []*oauth2.Token{initial, initial, refreshed}}
	store := new(recordingTokenStore)
	persistent := componenthttp.NewPersistentTokenSource(t.Context(), source, store)

	for range 3 {
		if _, err := persistent.Token(); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.tokens) != 2 {
		t.Fatalf("stored %d tokens, want initial and refreshed tokens", len(store.tokens))
	}
	if got := store.tokens[0].Extra("scope"); got != "tools.read" {
		t.Fatalf("stored initial token scope = %v", got)
	}
	if got := store.tokens[1]; got.AccessToken != "access-2" || got.RefreshToken != "refresh-2" || !got.Expiry.Equal(refreshed.Expiry) {
		t.Fatalf("stored refreshed token = %+v", got)
	}
	if got := store.tokens[1].Extra("scope"); got != "tools.read tools.write" {
		t.Fatalf("stored refreshed token scope = %v", got)
	}
}

func TestPersistentTokenSourceReturnsStorageFailure(t *testing.T) {
	wantErr := errors.New("database unavailable")
	source := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "new-token"})
	persistent := componenthttp.NewPersistentTokenSource(t.Context(), source, componenthttp.TokenStoreFunc(
		func(context.Context, *oauth2.Token) error { return wantErr },
	))

	token, err := persistent.Token()
	if token != nil {
		t.Fatalf("token = %+v, want nil after storage failure", token)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func (s *tokenSequence) Token() (*oauth2.Token, error) {
	if s.next >= len(s.tokens) {
		return s.tokens[len(s.tokens)-1], nil
	}
	token := s.tokens[s.next]
	s.next++
	return token, nil
}

func (s *recordingTokenStore) StoreToken(_ context.Context, token *oauth2.Token) error {
	clone := *token
	s.tokens = append(s.tokens, &clone)
	return nil
}
