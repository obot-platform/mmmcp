package mmmcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/obot-platform/mmmcp/component"
)

func TestStreamingAuthorizationOnlyRewritesSetup(t *testing.T) {
	for _, committed := range []bool{false, true} {
		composite := &Composite{}
		req := httptest.NewRequest(http.MethodPost, "http://example.test/mcp", strings.NewReader(`{"method":"subscriptions/listen"}`))
		rec := httptest.NewRecorder()
		composite.serveStreamingAuthorizationResponse(rec, req, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if committed {
				_, _ = w.Write([]byte("data: ready\n\n"))
				w.(http.Flusher).Flush()
				if !strings.Contains(rec.Body.String(), "ready") {
					t.Fatal("stream was buffered")
				}
			}

			composite.captureAuthorizationError(r.Context(), nil, &component.AuthorizationError{
				StatusCode: 401,
				Scope:      "read",
			})
			if !committed {
				w.WriteHeader(http.StatusInternalServerError)
			}

			_, _ = w.Write([]byte("data: failed\n\n"))
		}))
		if committed {
			if rec.Code != http.StatusOK || rec.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("committed stream rewritten: %d", rec.Code)
			}
		} else if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Header().Get("WWW-Authenticate"), `scope="read"`) {
			t.Fatalf("setup challenge lost: %d %s", rec.Code, rec.Body.String())
		}
	}
}
