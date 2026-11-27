// Package redact removes personal data and secrets from free text before
// anything is rendered (docs/ARCHITECTURE.md, sections 7 and 11).
//
// New returns a Redactor; String redacts one text and Map redacts the values
// of a map (a value whose key names a secret is replaced whole). Matches are
// replaced by fixed placeholders: [email], [phone], [card] and [token]. The
// placeholders are brackets and lower-case letters only, so no pattern can
// match them again, which makes String idempotent. A nil *Redactor redacts
// nothing, so callers can pass nil to switch redaction off.
//
// # What is redacted
//
//   - Passwords in URLs: in scheme://user:password@host only the password
//     becomes [token].
//   - Bearer tokens: the value after "Bearer" (RFC 6750 characters). A short
//     word made of letters only is prose and is kept ("Bearer token expired",
//     `Bearer realm="lab"`); the search then restarts at that word, so
//     "Bearer Bearer <token>" still loses the token.
//   - JWTs: three base64url segments, the first starting with eyJ (the
//     signature may be empty).
//   - GitHub tokens: ghp_, gho_, ghu_, ghs_ and ghr_ followed by at least 20
//     letters or digits, and github_pat_ tokens.
//   - AWS access key IDs: AKIA or ASIA followed by 16 upper-case letters or
//     digits.
//   - Secret key/value pairs: the value in key=value, key: value, "key":
//     "value" and JSON-escaped \"key\":\"value\", where the key contains
//     token, secret, password, passwd, api_key, apikey, api-key or
//     authorization (any case). A leading HTTP auth scheme is kept
//     ("Authorization: Basic [token]"). Keys right after '/' or '@' are host
//     names or paths, not keys (https://secrets.lab:8200).
//   - Email addresses, except file names that look like one (logo@2x.png).
//   - Phone numbers: '+' then 7 to 15 digits (E.164), and national numbers
//     with a trunk 0 then a non-zero digit, 10 or 11 digits in at most four
//     groups (024 123 4567, 0244123456). Groups may be joined by single
//     spaces, dashes or dots.
//   - Card numbers: 13 to 19 digits starting with 2 to 9 that pass the Luhn
//     check, ungrouped or as four digits then groups of three to six.
//
// # What is kept on purpose
//
// Output full of false positives is useless, so numbers are judged as whole
// runs of digit groups that stand on their own. Kept: timestamps and dates,
// durations (1005ms), HTTP status codes, versions (v1.4.0, 1.47.0), IP
// addresses and ports, decimals (0.0244123456), IDs that glue digits to
// letters (ord_000123, INV-2026-0244123456, ch_000042), trace and span IDs,
// short numbers and amounts (2500, 50,000.00), and URLs without credentials.
//
// # Known limits
//
// Redaction is a safety net, not a guarantee:
//   - Secrets with no known prefix and no secret-sounding key (a random API
//     key alone in a sentence) are not found.
//   - Phone numbers written without '+' and without a trunk 0, such as
//     415-555-0100, are not found; neither are card numbers split by dots.
//   - An unquoted secret value ends at the first space, comma, semicolon,
//     ampersand or quote.
//   - Names, postal addresses and other personal data in prose are not
//     detected.
package redact
