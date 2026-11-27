package fingerprint

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxExamples is how many distinct messages a Group keeps as examples.
const maxExamples = 3

// Group is a set of samples with the same class, service and signature.
type Group struct {
	Class     Class     `json:"class"`
	Service   string    `json:"service"`
	Signature string    `json:"signature"`
	RuleID    string    `json:"rule_id"` // the rule that decided most of the group's samples
	Count     int       `json:"count"`
	Share     float64   `json:"share"` // Count as a fraction of all samples, 0..1
	First     time.Time `json:"first"` // UTC
	Last      time.Time `json:"last"`  // UTC
	Examples  []string  `json:"examples,omitempty"`
}

// Report is what Group returns: every sample classified and grouped.
type Report struct {
	Total  int               `json:"total"`  // number of samples
	Groups []Group           `json:"groups"` // largest first
	Shares map[Class]float64 `json:"shares"` // fraction of all samples per class, 0..1; only classes seen
}

type groupKey struct {
	class              Class
	service, signature string
}

type groupBuilder struct {
	g        Group
	rules    map[string]int       // rule id -> samples it decided
	messages map[string]time.Time // distinct message -> earliest time
}

// Group classifies every sample and groups them by class, service and
// signature: the message with its numbers and IDs replaced by placeholders,
// or the exception type and HTTP status when there is no message (see the
// package comment).
//
// Groups are sorted by Count, largest first, then by Class, Service and
// Signature, so the same samples always give the same report. A group's
// RuleID is the rule that decided most of its samples (the smallest id on a
// tie), and its Examples are up to three distinct messages, earliest first.
func (rs *RuleSet) Group(samples []Sample) Report {
	builders := map[groupKey]*groupBuilder{}
	classCounts := map[Class]int{}
	for i := range samples {
		s := &samples[i]
		class, id := rs.Classify(*s)
		classCounts[class]++

		key := groupKey{class: class, service: s.Service, signature: signature(*s)}
		b := builders[key]
		t := s.Time.UTC()
		if b == nil {
			b = &groupBuilder{
				g:        Group{Class: class, Service: s.Service, Signature: key.signature, First: t, Last: t},
				rules:    map[string]int{},
				messages: map[string]time.Time{},
			}
			builders[key] = b
		}
		b.g.Count++
		if t.Before(b.g.First) {
			b.g.First = t
		}
		if t.After(b.g.Last) {
			b.g.Last = t
		}
		b.rules[id]++
		if msg := strings.TrimSpace(s.Message); msg != "" {
			if seen, ok := b.messages[msg]; !ok || t.Before(seen) {
				b.messages[msg] = t
			}
		}
	}

	total := len(samples)
	report := Report{Total: total, Groups: make([]Group, 0, len(builders)), Shares: make(map[Class]float64, len(classCounts))}
	for class, n := range classCounts {
		report.Shares[class] = float64(n) / float64(total)
	}
	for _, b := range builders {
		g := b.g
		g.Share = float64(g.Count) / float64(total)
		g.RuleID = topRule(b.rules)
		g.Examples = examples(b.messages)
		report.Groups = append(report.Groups, g)
	}
	slices.SortFunc(report.Groups, func(a, b Group) int {
		return cmp.Or(
			cmp.Compare(b.Count, a.Count),
			cmp.Compare(a.Class, b.Class),
			cmp.Compare(a.Service, b.Service),
			cmp.Compare(a.Signature, b.Signature),
		)
	})
	return report
}

// topRule returns the rule that decided the most samples; the smallest id wins a tie.
func topRule(counts map[string]int) string {
	best, bestN := "", 0
	for id, n := range counts {
		if n > bestN || (n == bestN && id < best) {
			best, bestN = id, n
		}
	}
	return best
}

// examples returns up to maxExamples messages, earliest first, then in
// alphabetical order, so the choice never depends on the samples' order.
func examples(first map[string]time.Time) []string {
	if len(first) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(first))
	for m := range first {
		msgs = append(msgs, m)
	}
	slices.SortFunc(msgs, func(a, b string) int {
		return cmp.Or(first[a].Compare(first[b]), strings.Compare(a, b))
	})
	return msgs[:min(len(msgs), maxExamples)]
}

// signature is what makes two errors "the same error": the sample's message
// with its variable parts replaced by placeholders, or its exception type and
// HTTP status when it has no message.
//
// UUIDs become <uuid>, timestamps <time>, IPv4 addresses <ip>, prefixed IDs
// such as ord_000123 or cus_42 <id>, hex IDs of eight or more characters (or
// with 0x) <hex>, and other whole numbers <n>, keeping a unit of up to three
// letters (1500ms becomes <n>ms). Digits inside words, as in x509 or v1, stay.
// Runs of white space become a single space, and the ends are trimmed.
func signature(s Sample) string {
	if sig := normalise(s.Message); sig != "" {
		return sig
	}
	var parts []string
	if t := strings.Join(strings.Fields(s.ExceptionType), " "); t != "" {
		parts = append(parts, t)
	}
	if s.HTTPStatus != 0 {
		parts = append(parts, "HTTP "+strconv.Itoa(s.HTTPStatus))
	}
	if len(parts) == 0 {
		return "(no message)"
	}
	return strings.Join(parts, " ")
}

// The variable parts of a message, replaced in this order. Every pattern
// starts and ends at a word boundary, so a placeholder never changes what its
// neighbours match and normalising twice gives the same result.
var (
	uuidPattern       = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	timePattern       = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2})?(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?\b`)
	ipPattern         = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	prefixedIDPattern = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_]*_[A-Za-z0-9_]*[0-9][A-Za-z0-9_]*\b`)
	hexPattern        = regexp.MustCompile(`\b(?:0[xX][0-9a-fA-F]+|[0-9a-fA-F]{8,})\b`)
	numberPattern     = regexp.MustCompile(`\b\d+(?:\.\d+)?([A-Za-z]{0,3})\b`) // keeps a unit: 3.5s -> <n>s
)

// normalise replaces the variable parts of a message; see signature.
func normalise(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if msg == "" {
		return ""
	}
	msg = uuidPattern.ReplaceAllString(msg, "<uuid>")
	msg = timePattern.ReplaceAllString(msg, "<time>")
	msg = ipPattern.ReplaceAllString(msg, "<ip>")
	msg = prefixedIDPattern.ReplaceAllString(msg, "<id>")
	msg = hexPattern.ReplaceAllStringFunc(msg, func(h string) string {
		if strings.HasPrefix(h, "0x") || strings.HasPrefix(h, "0X") ||
			(strings.ContainsAny(h, "0123456789") && strings.ContainsAny(h, "abcdefABCDEF")) {
			return "<hex>"
		}
		return h // only digits (a number, below) or only letters (a word)
	})
	return numberPattern.ReplaceAllString(msg, "<n>$1")
}
