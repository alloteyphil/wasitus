package main

import (
	"testing"
	"time"

	"github.com/alloteyphil/wasitus/internal/collector/signoz"
	"github.com/alloteyphil/wasitus/internal/correlate"
	"github.com/alloteyphil/wasitus/internal/event"
	"github.com/alloteyphil/wasitus/internal/fingerprint"
	"github.com/alloteyphil/wasitus/internal/traffic"
	"github.com/alloteyphil/wasitus/internal/verdict"
)

// These tests cover realistic incidents where the verdict used to blame the
// wrong thing: a mid-incident rollback, a stray error in another service, a
// retry storm read as a traffic surge, and a near-idle service's "spike".

var blameT0 = time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)

func blameChange(id, svc, changeType string, at time.Time) event.Event {
	return event.Event{
		ID: id, Time: at, Kind: event.KindChange, Source: "github", Service: svc,
		Title: id, Severity: event.SeverityInfo,
		Attrs: map[string]string{correlate.AttrChangeType: changeType},
	}
}

func blameSamples(n int, svc, msg string, start time.Time) []fingerprint.Sample {
	out := make([]fingerprint.Sample, n)
	for i := range out {
		out[i] = fingerprint.Sample{
			Time: start.Add(time.Duration(i) * time.Second), Service: svc, HTTPStatus: 500, Message: msg,
		}
	}
	return out
}

func blameDecide(t *testing.T, ss []fingerprint.Sample, events []event.Event, tr *traffic.Result, base map[fingerprint.Class]int) verdict.Result {
	t.Helper()
	rs, err := fingerprint.LoadRules(nil, fingerprint.Options{
		InternalHosts: []string{"storefront", "orders-service", "wallet-service"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rep := rs.Group(ss)
	win := event.Window{From: blameT0, To: blameT0.Add(15 * time.Minute)}
	return verdict.Decide(verdict.Input{
		Errors:         rep,
		Candidates:     rankChanges(events, rep, win, time.Hour),
		Traffic:        tr,
		BaselineErrors: base,
	}, verdict.DefaultConfig())
}

func wantBlamed(t *testing.T, v verdict.Result, id string) {
	t.Helper()
	if v.Side != verdict.SideOurSide || v.Cause != verdict.CauseChange {
		t.Fatalf("verdict = %s (%s), want OUR_SIDE / change\nevidence: %v", v.Label(), v.Confidence, v.Evidence)
	}
	if v.Change == nil || v.Change.Change.ID != id {
		got := "<nil>"
		if v.Change != nil {
			got = v.Change.Change.ID
		}
		t.Errorf("blamed %s, want %s", got, id)
	}
}

func TestMidIncidentRollbackIsNotBlamed(t *testing.T) {
	// Bad orders deploy at T-10m; orders panics from T. A responder deploys a
	// storefront rollback at T+3m; storefront's cascaded errors start at T+4m.
	ss := blameSamples(30, "orders-service", "runtime error: index out of range [1] with length 1", blameT0)
	ss = append(ss, blameSamples(5, "storefront", "HTTP 500", blameT0.Add(4*time.Minute))...)
	v := blameDecide(t, ss, []event.Event{
		blameChange("orders-v1.4.0", "orders-service", "deploy", blameT0.Add(-10*time.Minute)),
		blameChange("storefront-rollback", "storefront", "deploy", blameT0.Add(3*time.Minute)),
	}, nil, nil)
	wantBlamed(t, v, "orders-v1.4.0")
	for _, c := range v.Candidates {
		if c.Change.ID == "storefront-rollback" {
			t.Errorf("a change made after the incident started is still a candidate: %+v", c)
		}
	}
}

func TestStrayErrorDoesNotBlameItsService(t *testing.T) {
	// All code errors are in orders; one stray wallet error lands just after
	// an unrelated wallet config change.
	ss := blameSamples(30, "orders-service", "runtime error: invalid memory address or nil pointer dereference", blameT0)
	ss = append(ss, blameSamples(1, "wallet-service", "db: connection pool timeout", blameT0.Add(30*time.Second))...)
	v := blameDecide(t, ss, []event.Event{
		blameChange("orders-v2", "orders-service", "deploy", blameT0.Add(-10*time.Minute)),
		blameChange("wallet-flag", "wallet-service", "config", blameT0.Add(-1*time.Minute)),
	}, nil, nil)
	wantBlamed(t, v, "orders-v2")
}

func TestRetryStormIsNotATrafficSurge(t *testing.T) {
	// A bad deploy causes retries: traffic 3.2x and the error rate jumps from
	// 0.1% to 16%, with the same (code) error mix as baseline.
	ss := blameSamples(500, "orders-service", "runtime error: index out of range [1] with length 1", blameT0)
	tr := &traffic.Result{
		IsSpike: true, Factor: 3.2, WindowRequests: 3200, ExpectedRequests: 1000,
		WindowErrorRate: 0.16, BaselineErrorRate: 0.001, Reason: "requests 3.2x",
	}
	v := blameDecide(t, ss, []event.Event{
		blameChange("orders-v1.4.0", "orders-service", "deploy", blameT0.Add(-2*time.Minute)),
	}, tr, map[fingerprint.Class]int{fingerprint.ClassCode: 2})
	wantBlamed(t, v, "orders-v1.4.0")
}

func TestIdleServiceSpikeIsIgnored(t *testing.T) {
	// orders-service is crashing at normal traffic; an idle admin service gets
	// 2 requests per bucket against a zero baseline.
	win := map[string][]signoz.TrafficBucket{}
	base := map[string][]signoz.TrafficBucket{}
	seen := map[string]map[time.Time]bool{"orders-service": {}, "admin": {}}
	addBase := func(svc string, b signoz.TrafficBucket) {
		if !seen[svc][b.Start] {
			seen[svc][b.Start] = true
			base[svc] = append(base[svc], b)
		}
	}
	for i := range 3 {
		ts := blameT0.Add(time.Duration(i) * 5 * time.Minute)
		win["orders-service"] = append(win["orders-service"], signoz.TrafficBucket{Start: ts, Requests: 1000, Errors: 200})
		win["admin"] = append(win["admin"], signoz.TrafficBucket{Start: ts, Requests: 2})
		for d := 1; d <= 7; d++ {
			for n := -1; n <= 1; n++ {
				h := ts.Add(-time.Duration(d)*24*time.Hour + time.Duration(n)*5*time.Minute)
				addBase("orders-service", signoz.TrafficBucket{Start: h, Requests: 1000})
				addBase("admin", signoz.TrafficBucket{Start: h, Requests: 0})
			}
		}
	}
	tr := analyzeTraffic(win, base, traffic.DefaultConfig())
	if tr == nil || tr.IsSpike {
		t.Fatalf("analyzeTraffic = %+v, want a non-spike result", tr)
	}
	ss := blameSamples(50, "orders-service", "runtime error: index out of range", blameT0)
	v := blameDecide(t, ss, []event.Event{
		blameChange("orders-v1.4.0", "orders-service", "deploy", blameT0.Add(-3*time.Minute)),
	}, tr, map[fingerprint.Class]int{fingerprint.ClassCode: 3})
	wantBlamed(t, v, "orders-v1.4.0")
}
