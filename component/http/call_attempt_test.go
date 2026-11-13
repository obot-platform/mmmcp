package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Only a session 404 on an operation's first POST makes it replayable: a
// later POST, such as a multi-round-trip continuation, follows one that ran.
func TestCallAttemptReplaysOnlyFirstPOSTSessionRejection(t *testing.T) {
	for _, test := range []struct {
		name     string
		statuses []int
		want     bool
	}{
		{name: "first POST rejected", statuses: []int{http.StatusNotFound}, want: true},
		{name: "continuation rejected", statuses: []int{http.StatusOK, http.StatusNotFound}, want: false},
		{name: "sent and failed", statuses: []int{http.StatusOK}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			statuses := test.statuses
			component := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(statuses[0])
				statuses = statuses[1:]
			}))
			t.Cleanup(component.Close)

			attempt := &callAttempt{}
			ctx := context.WithValue(t.Context(), callAttemptKey{}, attempt)
			transport := headerTransport{base: http.DefaultTransport}
			for range test.statuses {
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, component.URL, strings.NewReader("{}"))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Mcp-Session-Id", "session")
				resp, err := transport.RoundTrip(req)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
			}

			// The SDK reports a lost session to every pending request.
			if got := attempt.replayable(mcp.ErrSessionMissing); got != test.want {
				t.Fatalf("replayable = %v, want %v", got, test.want)
			}
		})
	}
}

// An operation that sent nothing is replayable only once the connection is
// known to be dead.
func TestCallAttemptReplaysUnsentOnlyOnDeadConnection(t *testing.T) {
	attempt := &callAttempt{}
	for _, test := range []struct {
		err  error
		want bool
	}{
		{err: mcp.ErrConnectionClosed, want: true},
		{err: mcp.ErrSessionMissing, want: true},
		{err: errors.New("invalid params"), want: false},
	} {
		if got := attempt.replayable(test.err); got != test.want {
			t.Errorf("replayable(%v) = %v, want %v", test.err, got, test.want)
		}
	}
}
