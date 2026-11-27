package redact

import (
	"regexp"
	"strings"
)

var (
	// secretNameRe finds the words that make a key name a secret.
	secretNameRe = regexp.MustCompile(`(?i)token|secret|passw(?:or)?d|api[_-]?key|authorization`)

	// secretKeyRe matches a key that names a secret, its optional closing
	// quote (plain or JSON-escaped) and the separator, up to where the
	// value starts: password=, "client_secret": , X-Api-Key: , \"token\":.
	secretKeyRe = regexp.MustCompile(`(?i)[a-z0-9_.-]*(?:token|secret|passw(?:or)?d|api[_-]?key|authorization)[a-z0-9_.-]*(?:\\?["'])?[ \t]*[:=][ \t]*`)

	// authSchemeRe matches an HTTP authentication scheme at the start of a
	// value, which is kept: "Authorization: Basic [token]".
	authSchemeRe = regexp.MustCompile(`(?i)^(?:bearer|basic|digest|negotiate|ntlm|token)[ \t]+`)
)

// redactKeyValues replaces the value in key=value, key: value and
// "key": "value" wherever the key names a secret.
func redactKeyValues(s string) string {
	var b strings.Builder
	last, pos := 0, 0
	for pos < len(s) {
		loc := secretKeyRe.FindStringIndex(s[pos:])
		if loc == nil {
			break
		}
		keyStart, valueAt := pos+loc[0], pos+loc[1]
		pos = valueAt
		if keyStart > 0 && (s[keyStart-1] == '/' || s[keyStart-1] == '@') {
			// A host or path segment, as in https://secrets.lab:8200/v1.
			continue
		}
		start, end := valueBounds(s, valueAt)
		if start == end || isPlaceholder(s[start:end]) {
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

// valueBounds returns where the value that starts at i begins and ends,
// leaving out its quotes and a leading authentication scheme. A quoted value
// ends at its closing quote or at the end of the line; an unquoted one at
// white space or one of " ' , ; &.
func valueBounds(s string, i int) (start, end int) {
	if i >= len(s) {
		return i, i
	}
	switch {
	case strings.HasPrefix(s[i:], `\"`): // JSON inside a JSON string: {\"password\":\"...\"}
		start = i + 2
		end = start
		for end < len(s) && s[end] != '\n' && !strings.HasPrefix(s[end:], `\"`) {
			end++
		}
	case s[i] == '"' || s[i] == '\'':
		quote := s[i]
		start = i + 1
		end = start
		for end < len(s) && s[end] != quote && s[end] != '\n' {
			if s[end] == '\\' && end+1 < len(s) && s[end+1] != '\n' {
				end++ // skip the escaped character
			}
			end++
		}
	default:
		start = skipAuthScheme(s, i, len(s))
		end = start
		for end < len(s) && !isValueDelimiter(s[end]) {
			end++
		}
		return start, start + len(trimUnbalancedClosers(s[start:end]))
	}
	return skipAuthScheme(s, start, end), end
}

// skipAuthScheme returns where the value in s[i:limit] starts once a leading
// authentication scheme and the blanks after it are skipped. A scheme with
// nothing after it is the value itself.
func skipAuthScheme(s string, i, limit int) int {
	m := authSchemeRe.FindStringIndex(s[i:limit])
	if m == nil || i+m[1] == limit || isValueDelimiter(s[i+m[1]]) {
		return i
	}
	return i + m[1]
}

func isValueDelimiter(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\v', '\f', '"', '\'', ',', ';', '&':
		return true
	}
	return false
}

// trimUnbalancedClosers drops closing brackets at the end of v that have no
// opening partner in v, so "map[password:x]" keeps its "]".
func trimUnbalancedClosers(v string) string {
	extra := map[byte]int{
		')': strings.Count(v, ")") - strings.Count(v, "("),
		']': strings.Count(v, "]") - strings.Count(v, "["),
		'}': strings.Count(v, "}") - strings.Count(v, "{"),
	}
	for v != "" && extra[v[len(v)-1]] > 0 {
		extra[v[len(v)-1]]--
		v = v[:len(v)-1]
	}
	return v
}
