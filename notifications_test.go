package mmmcp

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/obot-platform/mmmcp/catalog"
)

func TestConfigToolSubscriptionsReleaseSnapshots(t *testing.T) {
	var subscriptions configToolSubscriptions
	compiled := &catalog.Catalog{}
	first, second := &mcp.Server{}, &mcp.Server{}
	if cleanup := subscriptions.observe("id", "tools/list", first, compiled, "same"); cleanup != nil || len(subscriptions.configs) != 0 {
		t.Fatal("request without listeners retained tracking")
	}
	closeFirst := subscriptions.observe("id", subscriptionsListenMethod, first, compiled, "same")
	closeSecond := subscriptions.observe("id", subscriptionsListenMethod, second, compiled, "same")
	closeFirst()
	if entry := subscriptions.configs["id"]; entry == nil || len(entry.servers) != 1 {
		t.Fatal("closing one listener removed the remaining listener")
	}
	closeSecond()
	if len(subscriptions.configs) != 0 {
		t.Fatal("last listener retained its configuration snapshot")
	}
}
