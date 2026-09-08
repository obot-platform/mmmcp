package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type info struct {
	PID        int                 `json:"pid"`
	Env        map[string]string   `json:"env"`
	ClientInfo *mcp.Implementation `json:"clientInfo"`
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "stdio-helper", Version: "1.0.0"}, nil)
	server.AddTool(&mcp.Tool{Name: "info", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		env := make(map[string]string)
		for _, entry := range os.Environ() {
			for i := range entry {
				if entry[i] == '=' {
					env[entry[:i]] = entry[i+1:]
					break
				}
			}
		}
		data, _ := json.Marshal(info{PID: os.Getpid(), Env: env, ClientInfo: req.ClientInfo()})
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
	})
	server.AddTool(&mcp.Tool{Name: "block", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
