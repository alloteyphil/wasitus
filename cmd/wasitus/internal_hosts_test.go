package main

import (
	"slices"
	"testing"

	"github.com/alloteyphil/wasitus/internal/collector/signoz"
	"github.com/alloteyphil/wasitus/internal/config"
	"github.com/alloteyphil/wasitus/internal/event"
)

func TestCollectInternalHosts(t *testing.T) {
	cfg := &config.Config{
		Services:      map[string]string{"a/b": "orders-service"},
		InternalHosts: []string{" billing-db ", ".corp.example", ""},
	}
	events := []event.Event{
		{Kind: event.KindChange, Service: "wallet-service"},
		{Kind: event.KindExternal, Service: "mockpay"},  // external notes never make a host ours
		{Kind: event.KindChange, Service: ".evil.test"}, // event data may not add a domain
	}
	samples := []signoz.ErrorSample{{Service: "storefront"}}

	got := collectInternalHosts(cfg, events, samples, nil, nil)
	for _, want := range []string{"billing-db", ".corp.example", "orders-service", "wallet-service", "storefront", "localhost"} {
		if !slices.Contains(got, want) {
			t.Errorf("hosts %v missing %q", got, want)
		}
	}
	for _, bad := range []string{"mockpay", ".evil.test", ""} {
		if slices.Contains(got, bad) {
			t.Errorf("hosts %v contain %q", got, bad)
		}
	}
}
