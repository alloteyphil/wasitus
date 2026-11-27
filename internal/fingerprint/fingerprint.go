package fingerprint

import (
	_ "embed" // for the built-in rules
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// Class is the kind of cause behind an error.
type Class string

// The cause classes a rule can give.
const (
	ClassCode       Class = "code"       // a fault in our own services
	ClassDependency Class = "dependency" // a third party failing: outage, timeout, unreachable
	ClassAccount    Class = "account"    // our account with a provider: suspended, unpaid, not enabled
	ClassLimit      Class = "limit"      // rate limits and quotas
	ClassExpiry     Class = "expiry"     // expired or unverifiable certificates, tokens and keys
)

// Valid reports whether c is one of the classes above.
func (c Class) Valid() bool {
	switch c {
	case ClassCode, ClassDependency, ClassAccount, ClassLimit, ClassExpiry:
		return true
	}
	return false
}

// Rule IDs that Classify reports when no rule matches. Rules may not use them.
const (
	FallbackOutbound = "fallback-outbound" // a call to a third party failed
	FallbackOurs     = "fallback-ours"     // an inbound request, or a call to our own service, failed
)

// Sample is one error seen in telemetry: an error span or an error log record.
type Sample struct {
	Time          time.Time         `json:"time"`
	Service       string            `json:"service"`                  // the service that reported the error
	Outbound      bool              `json:"outbound,omitempty"`       // a client span: Service called someone
	HTTPStatus    int               `json:"http_status,omitempty"`    // 0 when unknown
	ServerAddress string            `json:"server_address,omitempty"` // who Service called (outbound only)
	ExceptionType string            `json:"exception_type,omitempty"`
	Message       string            `json:"message,omitempty"`
	Attrs         map[string]string `json:"attrs,omitempty"`
}

// Options tune how samples are classified.
type Options struct {
	// InternalHosts are the host names of our own services, such as
	// "orders-service" or "localhost". A failed call to one of them is our
	// own problem, not a third party's. Matching ignores case and ports. An
	// entry that starts with a dot, such as ".svc.cluster.local", matches
	// that domain and every host under it.
	InternalHosts []string
}

// RuleSet is an ordered list of rules and the options they run with. It is
// safe for concurrent use.
type RuleSet struct {
	rules    []rule
	internal hostSet
}

//go:embed rules.yaml
var builtinRules []byte

// LoadRules parses the user's rules and the built-in rules into one ordered
// set: user rules first, so they win, then the built-ins. userRules may be
// empty. Every problem found is reported, each naming its rule; two rules
// with the same id are an error, also when one is a built-in.
func LoadRules(userRules []byte, opts Options) (*RuleSet, error) {
	return load(userRules, builtinRules, opts)
}

func load(user, builtin []byte, opts Options) (*RuleSet, error) {
	userSet, userErr := parseRules(user, "user rules")
	builtinSet, builtinErr := parseRules(builtin, "built-in rules")
	errs := []error{userErr, builtinErr}

	rules := make([]rule, 0, len(userSet)+len(builtinSet))
	rules = append(rules, userSet...)
	rules = append(rules, builtinSet...)
	first := make(map[string]*rule, len(rules))
	for i := range rules {
		r := &rules[i]
		if f, dup := first[r.id]; dup {
			errs = append(errs, fmt.Errorf("duplicate rule id %q: %s line %d and %s line %d", r.id, f.source, f.line, r.source, r.line))
			continue
		}
		first[r.id] = r
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &RuleSet{rules: rules, internal: newHostSet(opts.InternalHosts)}, nil
}

// Classify returns the class of one sample and the id of the rule that
// decided it. The first matching rule wins. When none matches, a failed call
// to a host that is not ours is ClassDependency (FallbackOutbound), and
// anything else is ClassCode (FallbackOurs).
func (rs *RuleSet) Classify(s Sample) (class Class, ruleID string) {
	in := rs.prepare(&s)
	for i := range rs.rules {
		if rs.rules[i].matches(&in) {
			return rs.rules[i].class, rs.rules[i].id
		}
	}
	if s.Outbound && !rs.internal.contains(in.peer) {
		return ClassDependency, FallbackOutbound
	}
	return ClassCode, FallbackOurs
}

// input is a sample prepared for matching: lower-cased text and its peer.
type input struct {
	s             *Sample
	message       string // lower-case
	exceptionType string // lower-case
	serverAddress string // lower-case
	peer          string // host the failed call went to, or ""
	thirdParty    bool   // peer is known and is not one of our hosts
}

func (rs *RuleSet) prepare(s *Sample) input {
	peer := peerOf(s)
	return input{
		s:             s,
		message:       strings.ToLower(s.Message),
		exceptionType: strings.ToLower(s.ExceptionType),
		serverAddress: strings.ToLower(s.ServerAddress),
		peer:          peer,
		thirdParty:    peer != "" && !rs.internal.contains(peer),
	}
}

// peerOf returns the host a failed call went to: the server address of an
// outbound sample, or else the host of the last URL in the message. Error
// messages wrap from the outside in (Go's client writes
// `Post "http://mockpay:8090/v1/charges": context deadline exceeded`), so the
// last URL is the innermost call, the one that failed first.
func peerOf(s *Sample) string {
	if s.Outbound {
		if h := hostOf(s.ServerAddress); h != "" {
			return h
		}
	}
	return lastURLHost(s.Message)
}

// urlAuthority finds the host[:port] part of URLs in free text. Quotes and
// backslashes end it, so URLs inside escaped JSON are found too.
var urlAuthority = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://([^\s/?#"'<>\\]+)`)

func lastURLHost(msg string) string {
	all := urlAuthority.FindAllStringSubmatch(msg, -1)
	if len(all) == 0 {
		return ""
	}
	return hostOf(all[len(all)-1][1])
}

// hostOf reduces an address ("mockpay", "mockpay:8090", "[::1]:8080",
// "http://user@mockpay:8090/x") to its lower-case host name.
func hostOf(addr string) string {
	h := strings.ToLower(strings.TrimSpace(addr))
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if i := strings.LastIndex(h, "@"); i >= 0 {
		h = h[i+1:]
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.TrimSuffix(strings.Trim(h, "[]"), ".")
}

// hostSet is the set of our own hosts.
type hostSet struct {
	exact    map[string]bool
	suffixes []string // ".svc.cluster.local": that domain and any host under it
}

func newHostSet(hosts []string) hostSet {
	set := hostSet{exact: make(map[string]bool, len(hosts))}
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if domain, ok := strings.CutPrefix(h, "."); ok {
			if d := hostOf(domain); d != "" {
				set.suffixes = append(set.suffixes, "."+d)
			}
			continue
		}
		if h := hostOf(h); h != "" {
			set.exact[h] = true
		}
	}
	return set
}

func (hs hostSet) contains(host string) bool {
	if host == "" {
		return false
	}
	if hs.exact[host] {
		return true
	}
	for _, suffix := range hs.suffixes {
		if host == suffix[1:] || strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
