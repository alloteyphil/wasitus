package redact

import (
	"regexp"
	"strings"
)

// Placeholders that replace redacted text. They are made of brackets and
// lower-case letters only, so no pattern can match them again.
const (
	email = "[email]"
	phone = "[phone]"
	card  = "[card]"
	token = "[token]"
)

// maxPasses bounds how often String runs the patterns over a text. One pass
// is enough for almost every input; a second pass only finds something when
// one redaction uncovered another, such as a card number that was glued to a
// phone number. The bound keeps a pathological input from looping for long.
const maxPasses = 8

// Redactor removes personal data and secrets from text. Create one with New;
// it is safe for concurrent use. A nil *Redactor redacts nothing, so a caller
// can pass nil to switch redaction off.
type Redactor struct {
	steps []func(string) string
}

// New returns a Redactor with the built-in patterns described in the package
// documentation.
func New() *Redactor {
	return &Redactor{steps: []func(string) string{
		redactURLPasswords,
		redactBearer,
		redactJWTs,
		redactGitHubTokens,
		redactAWSKeyIDs,
		redactKnownSecrets,
		redactSecretQueries,
		redactKeyValues,
		redactEmails,
		redactBracketPhones,
		redactNumbers,
		redactBareMSISDNs, // after redactNumbers, so card numbers are claimed first
	}}
}

// String returns s with every match replaced by its placeholder. It is
// idempotent: String(String(s)) == String(s).
func (r *Redactor) String(s string) string {
	if r == nil {
		return s
	}
	for range maxPasses {
		next := r.pass(s)
		if next == s {
			break
		}
		s = next
	}
	return s
}

// Map returns a new map with every value redacted; m is not changed. A value
// whose key names a secret (the key contains token, secret, password, passwd,
// api_key, apikey, api-key or authorization) is replaced whole with [token],
// whatever it looks like. Keys are kept as they are. Map(nil) returns nil.
func (r *Redactor) Map(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if r != nil && v != "" && secretNameRe.MatchString(k) {
			out[k] = token
			continue
		}
		out[k] = r.String(v)
	}
	return out
}

func (r *Redactor) pass(s string) string {
	for _, step := range r.steps {
		s = step(s)
	}
	return s
}

func isPlaceholder(s string) bool {
	return s == email || s == phone || s == card || s == token
}

var (
	// scheme://user:password@ : group 1 is the password. Brackets are not
	// allowed in URL user info, which also keeps [token] from matching.
	urlPasswordRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s:/?#@\[\]]*:([^\s/?#@\[\]]+)@`)

	// "Bearer <value>", value as in RFC 6750 (b64token): group 1.
	bearerRe = regexp.MustCompile(`(?i)\bbearer[ \t]+([a-z0-9._~+/-]+=*)`)

	// A word such as "token" or "Realm": what follows "bearer" in prose.
	proseWordRe = regexp.MustCompile(`^[A-Za-z][a-z]*$`)

	// Three base64url segments, the first a JSON header ("eyJ" is "{"").
	// The signature may be empty (unsigned tokens end with a dot).
	jwtRe = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)

	// GitHub tokens: classic (ghp_, gho_, ghu_, ghs_, ghr_) and fine-grained.
	githubTokenRe = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`)

	// AWS access key IDs: long-term (AKIA) and temporary (ASIA).
	awsKeyIDRe = regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)

	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

	// Secrets with a well-known shape: PEM private keys (the whole block,
	// or from BEGIN to the end of the text when it was cut short), Slack
	// tokens, Stripe secret and restricted keys, Google API keys, and Slack
	// incoming-webhook paths.
	knownSecretRe = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----(?:(?s:.*?)-----END [A-Z ]*PRIVATE KEY-----|(?s:.*))` +
		`|\bxox[abprs]-[A-Za-z0-9-]{10,}` +
		`|\b(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{10,}` +
		`|\bAIza[A-Za-z0-9_-]{35}` +
		`|hooks\.slack\.com/services/[A-Za-z0-9/]+`)

	// Credentials carried in a URL query string: signed-URL signatures
	// (AWS, Azure SAS, GCS) and API keys or tokens. Group 1 is the value.
	// Phone numbers with an area code in brackets: "(024) 123 4567",
	// "+1 (415) 555-0100", "+44 (0)20 7946 0958". scanRun does not treat
	// brackets as separators, so these need their own pattern.
	bracketPhoneRe = regexp.MustCompile(`(?:\+\d{1,3}[ ]?)?\(\d{1,4}\)[ ]?\d{2,4}(?:[ -]\d{2,4}){1,3}\b`)

	// MSISDNs written without '+', as mobile-money and telco logs do:
	// "00233241234567", or an African country code (2xx) and 8 to 10 more
	// digits, "233241234567". Other countries' bare numbers stay, because a
	// bare 11-13 digit number is as likely an ID or a millisecond time.
	bareMSISDNRe = regexp.MustCompile(`\b(?:00[1-9]\d{8,13}|2\d{2}[1-9]\d{7,9})\b`)

	secretQueryRe = regexp.MustCompile(`(?i)[?&](?:x-amz-signature|x-amz-credential|x-amz-security-token|x-goog-signature|sig|signature|key|api_key|access_token)=([^&\s#"'<>]+)`)
)

// fileExtensions are "top-level domains" that show an email-like match is a
// file name such as logo@2x.png.
var fileExtensions = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true, "svg": true, "webp": true,
	"ico": true, "css": true, "js": true, "json": true, "html": true, "txt": true,
	"map": true, "log": true,
}

func redactURLPasswords(s string) string {
	return replaceMatches(s, urlPasswordRe, 1, token, nil)
}

// redactBearer replaces the value after "Bearer". It cannot use
// replaceMatches: regexp matches never overlap, so in "Bearer Bearer <token>"
// the first match would swallow the second "Bearer" as its (prose) value and
// the real token would never be looked at. When the value turns out to be a
// prose word, the search restarts at that word, which may itself be "Bearer".
func redactBearer(s string) string {
	var b strings.Builder
	last, pos := 0, 0
	for pos < len(s) {
		m := bearerRe.FindStringSubmatchIndex(s[pos:])
		if m == nil {
			break
		}
		start, end := pos+m[2], pos+m[3]
		if isProseWord(s[start:end]) {
			pos = start // always > previous pos: the match includes "bearer" first
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(token)
		last, pos = end, end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// isProseWord reports whether the word after "Bearer" is prose ("Bearer token
// expired", `Bearer realm="lab"`) rather than a credential.
func isProseWord(v string) bool {
	w := strings.TrimRight(v, "=")
	return len(w) < 20 && proseWordRe.MatchString(w)
}

func redactJWTs(s string) string {
	return replaceMatches(s, jwtRe, 0, token, nil)
}

func redactGitHubTokens(s string) string {
	return replaceMatches(s, githubTokenRe, 0, token, nil)
}

func redactAWSKeyIDs(s string) string {
	return replaceMatches(s, awsKeyIDRe, 0, token, nil)
}

// redactBracketPhones replaces bracketed-area-code phone numbers with 7 to
// 15 digits.
func redactBracketPhones(s string) string {
	return replaceMatches(s, bracketPhoneRe, 0, phone, func(m string) bool {
		n := 0
		for _, c := range m {
			if c >= '0' && c <= '9' {
				n++
			}
		}
		return n < 7 || n > 15 // keep: too short or too long to be a phone
	})
}

func redactBareMSISDNs(s string) string {
	return replaceMatches(s, bareMSISDNRe, 0, phone, nil)
}

func redactKnownSecrets(s string) string {
	return replaceMatches(s, knownSecretRe, 0, token, nil)
}

func redactSecretQueries(s string) string {
	return replaceMatches(s, secretQueryRe, 1, token, nil)
}

// redactEmails replaces email addresses. Two things that look like one are
// kept: file names (logo@2x.png), and the user@host of a remote, which is a
// login name and a host rather than a mailbox: ssh://git@github.com/org/repo
// (user info right after "://") and git@github.com:org/repo.git (followed by
// ':' and a path). Anything else after the ':' ("alice@example.com:443",
// "...: thanks") leaves it an address.
func redactEmails(s string) string {
	matches := emailRe.FindAllStringIndex(s, -1)
	if matches == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		if isFileName(s[m[0]:m[1]]) || isRemoteLogin(s, m[0], m[1]) {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(email)
		last = m[1]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func isFileName(m string) bool {
	return fileExtensions[strings.ToLower(m[strings.LastIndexByte(m, '.')+1:])]
}

// isRemoteLogin reports whether the email-like s[start:end] is the user@host
// of a URL or an scp-style remote.
func isRemoteLogin(s string, start, end int) bool {
	if strings.HasSuffix(s[:start], "://") {
		return true
	}
	// scp-style remote: user@host:path. A path is required (a '/' or a
	// repository name ending in .git), so "alice@example.com:hunter2" and
	// "bob@example.com:443" are still addresses.
	if end >= len(s) || s[end] != ':' {
		return false
	}
	rest := s[end+1:]
	if i := strings.IndexAny(rest, " \t\r\n"); i >= 0 {
		rest = rest[:i]
	}
	return strings.Contains(rest, "/") || strings.HasSuffix(rest, ".git")
}

// replaceMatches replaces submatch group of every match of re in s with
// placeholder, except the matches for which keep (when not nil) returns true.
func replaceMatches(s string, re *regexp.Regexp, group int, placeholder string, keep func(string) bool) string {
	matches := re.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, m := range matches {
		start, end := m[2*group], m[2*group+1]
		if keep != nil && keep(s[start:end]) {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(placeholder)
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}
