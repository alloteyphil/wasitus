package correlate_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/alloteyphil/wasitus/internal/correlate"
	"github.com/alloteyphil/wasitus/internal/event"
)

func ExampleRank() {
	first := time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)
	events := []event.Event{
		{
			ID: "docs-7", Time: first.Add(-5 * time.Minute), Kind: event.KindChange,
			Source: "github", Service: "orders-service", Title: "Fix README typo",
			Attrs: map[string]string{correlate.AttrChangeType: "docs"},
		},
		{
			ID: "deploy-42", Time: first.Add(-17 * time.Minute), Kind: event.KindChange,
			Source: "github", Service: "orders-service", Title: "Deploy orders-service v1.4.0",
			Attrs: map[string]string{correlate.AttrChangeType: "deploy"},
		},
		{
			ID: "config-3", Time: first.Add(-40 * time.Minute), Kind: event.KindChange,
			Source: "changes-file", Service: "wallet-service", Title: "Raise mockpay timeout",
			Attrs: map[string]string{correlate.AttrChangeType: "config"},
		},
		{
			ID: "alert-1", Time: first, Kind: event.KindSymptom,
			Source: "signoz", Service: "orders-service", Title: "Error rate above 5%",
		},
	}

	symptom := correlate.Symptom{Start: first, Service: "orders-service"}
	for _, c := range correlate.Rank(symptom, events, correlate.DefaultWeights()) {
		fmt.Printf("%.2f %s: %s\n", c.Score, c.Change.ID, strings.Join(c.Reasons, "; "))
	}
	// Output:
	// 0.68 docs-7: 5m before the first symptom; same service: orders-service; type docs (weight 0.1)
	// 0.66 deploy-42: 17m before the first symptom; same service: orders-service; type deploy (weight 1.0)
	// 0.19 config-3: 40m before the first symptom; different service: wallet-service (symptom in orders-service); type config (weight 0.8)
}
