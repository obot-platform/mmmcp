package mmmcp

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RunStdio serves one persistent MCP frontend session over stdin and stdout.
// A configuration attached to ctx is authoritative for the entire session.
func (c *Composite) RunStdio(ctx context.Context) error {
	return c.runPersistent(ctx, &mcp.StdioTransport{})
}

func (c *Composite) runPersistent(ctx context.Context, transport mcp.Transport) error {
	if c == nil {
		return errors.New("mmmcp: nil composite server")
	}
	if ctx == nil {
		return errors.New("mmmcp: nil context")
	}
	if transport == nil {
		return errors.New("mmmcp: nil transport")
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.stdioMu.Lock()
	if c.closed.Load() {
		c.stdioMu.Unlock()
		cancel()
		return errors.New("mmmcp: composite server is closed")
	}
	c.stdioNext++
	id := c.stdioNext
	c.stdioCancels[id] = cancel
	c.stdioWG.Add(1)
	c.stdioMu.Unlock()

	defer func() {
		cancel()
		c.stdioMu.Lock()
		delete(c.stdioCancels, id)
		c.stdioMu.Unlock()
		c.stdioWG.Done()
	}()

	cfg := effectiveConfig(runCtx, c.defaultConfig)
	compiled, _, err := c.registry.Get(runCtx, cfg)
	if err != nil {
		return err
	}
	server := newFrontendServer(c, c.serverOptions, frontendImplementation(cfg, compiled))
	return server.Run(runCtx, transport)
}
