package api

import (
	"net/url"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
)

func TestSubscriptionAddressUsesOwnSiteDomain(t *testing.T) {
	node := models.Node{IP: "203.0.113.11", SelfStealDomain: "site.example.com", SNI: "site.example.com", CDNDomain: "cdn.example.com"}
	for _, tt := range []struct {
		server subServer
		host   string
	}{
		{subServer{Node: node}, "site.example.com"},
		{subServer{Node: node, XHTTPPath: "/path"}, "site.example.com"},
		{subServer{Node: node, XHTTPPath: "/path", CDN: true}, "cdn.example.com"},
	} {
		link, err := url.Parse(vlessLink("client", tt.server))
		if err != nil || link.Hostname() != tt.host {
			t.Fatalf("VLESS host = %v, %v, want %s", link, err, tt.host)
		}
	}
	node.Domain = "connect.example.com"
	if got := nodeAddress(node); got != node.Domain {
		t.Fatalf("explicit address = %s, want %s", got, node.Domain)
	}
	node.Domain, node.SelfStealDomain = "", ""
	if got := nodeAddress(node); got != node.IP {
		t.Fatalf("address without a domain = %s, want %s", got, node.IP)
	}
}
