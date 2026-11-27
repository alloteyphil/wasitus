// Package fingerprint sorts error samples into cause classes, so the verdict
// can tell failures in our own services from failures somewhere else.
//
// # Classes
//
//	code        a fault in our own services
//	dependency  a third party failing: unavailable, timing out, unreachable
//	account     our account with a provider: suspended, unpaid, not enabled, denied
//	limit       rate limits and quotas
//	expiry      certificates, tokens and keys that expired or fail to verify
//
// Traffic spikes, the sixth cause in the design, are found from request rates
// by another package, not from error samples.
//
// # Rules
//
// Rules are data, not code. The built-in rules live in rules.yaml, embedded in
// the binary, and users add their own for their providers' error formats
// (LoadRules). A rule file is a YAML list:
//
//	# rules.local.yaml
//	- id: provider-suspended      # letters, digits, '.', '_' and '-'
//	  class: account              # code, dependency, account, limit or expiry
//	  match:
//	    all:                      # every entry must hold ...
//	      - outbound: true
//	    any:                      # ... and, if given, at least one of these
//	      - message_contains: AccountSuspended
//	      - {http_status: 402, server_address_contains: mockpay}
//
// A match needs an any: list, an all: list, or both. Each list entry is a
// condition; an entry with several keys holds only when all of them hold:
//
//	message_contains         the message contains the text, ignoring case
//	exception_type           the exception type is exactly this
//	exception_type_contains  the exception type contains the text, ignoring case
//	http_status              the span's HTTP status code
//	outbound                 true for a client span (we called someone), false otherwise
//	server_address_contains  the address we called contains the text, ignoring case
//	attr_equals              every listed attribute is present with exactly this value
//	third_party              true when the failed call went to a host that is not ours
//
// Decoding is strict: an unknown key, an unknown class, an empty match or
// condition, or a rule without an id is an error that names the rule, and so
// is an id used twice. Every bad rule is reported, not only the first.
//
// # Our hosts and third parties
//
// Options.InternalHosts lists the host names of our own services. A failed
// call to one of them is a failure inside our system, not a third party's.
// The host a failed call went to is the server address of an outbound
// sample, or else the host of the last URL in the message: Go's HTTP client
// writes errors like
//
//	Post "http://mockpay:8090/v1/charges": context deadline exceeded
//
// and each service that passes an error on wraps it in its own words, so the
// last URL belongs to the innermost call, the one that failed first. The
// third_party condition is how a rule asks "was this a call to someone else?",
// which the rule file cannot know by itself.
//
// # Propagated errors and rule order
//
// A failure deep in a call chain is reported by every service it passes
// through. When mockpay suspends the lab's account, wallet-service answers
// 502 with "mockpay rejected the charge: HTTP 402 AccountSuspended: ...", and
// orders-service and storefront answer 502 with that whole text wrapped in
// their own ("could not place the order: HTTP 502 PaymentFailed: payment for
// ord_000001 failed: HTTP 502 PaymentProviderError: mockpay rejected the
// charge: HTTP 402 AccountSuspended: ..."). Rules therefore match the
// root-cause words anywhere in the message, so every service in the chain
// gets the root cause's class instead of blaming the hop next to it.
//
// The first rule that matches decides, so order matters. A message can hold
// both our wrapper words and the root cause's, so the built-ins never match
// wrapper text (such as "HTTP 502" from our own services), list specific
// causes (account, limit, expiry) before generic ones (timeouts, refused
// connections), and leave failures that start in our own code to the code
// rules and the fallback. User rules come before the built-ins, so a user's
// rule wins over a built-in one that matches the same sample.
//
// # Fallbacks
//
// When no rule matches, an outbound sample whose host is not one of ours (or
// is unknown) is ClassDependency, reported as rule FallbackOutbound; any other
// sample, inbound or a call to our own host, is ClassCode (FallbackOurs).
//
// # Groups
//
// RuleSet.Group classifies a set of samples and groups them by class, service
// and signature, with each group's share of all samples and each class's
// share (Report.Shares), which is the evidence the verdict weighs. The
// signature is the message with its variable parts replaced, so one error
// seen for many orders is one group:
//
//	message:   payment for ord_000123 failed after 2026-11-26T10:00:00Z: HTTP 504
//	signature: payment for <id> failed after <time>: HTTP <n>
//
// UUIDs become <uuid>, timestamps <time>, IPv4 addresses <ip>, prefixed IDs
// such as ord_000123 <id>, hex IDs <hex> and other numbers <n>. A sample
// without a message is signed by its exception type and HTTP status instead.
package fingerprint
