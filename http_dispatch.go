package mmmcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

const (
	currentProtocolVersion = "2026-07-28"
)

type httpDispatcher struct {
	stateless http.Handler
	stateful  http.Handler
}

func (d *httpDispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if requestIsStateless(r) {
		d.stateless.ServeHTTP(w, r)
		return
	}
	d.stateful.ServeHTTP(w, r)
}

func requestIsStateless(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	if r.Header.Get("Mcp-Protocol-Version") >= currentProtocolVersion {
		return true
	}
	if r.Header.Get("Mcp-Session-Id") != "" {
		return false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	var messages []struct {
		Method string `json:"method"`
		Params struct {
			Meta map[string]any `json:"_meta"`
		} `json:"params"`
	}
	if len(body) > 0 && body[0] == '[' {
		if err := json.Unmarshal(body, &messages); err != nil {
			return false
		}
	} else {
		var message struct {
			Method string `json:"method"`
			Params struct {
				Meta map[string]any `json:"_meta"`
			} `json:"params"`
		}
		if err := json.Unmarshal(body, &message); err != nil {
			return false
		}
		messages = append(messages, message)
	}
	for _, message := range messages {
		if message.Method == "server/discover" {
			return true
		}
		if version, _ := message.Params.Meta["io.modelcontextprotocol/protocolVersion"].(string); version >= currentProtocolVersion {
			return true
		}
	}
	return false
}
