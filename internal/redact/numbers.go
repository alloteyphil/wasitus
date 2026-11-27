package redact

import (
	"slices"
	"strings"
)

// A digit run is a sequence of digit groups joined by single separators
// (space, '-' or '.'), optionally after a '+': "+233 24 123 4567",
// "2026-11-14 03", "192.168.68.57". redactNumbers finds the runs that stand
// on their own and then looks inside each one for, in this order, an
// international phone number at the start, card numbers, and local phone
// numbers. Working on whole groups keeps dates, IPs and versions intact.

// span is a half-open byte range of s.
type span struct{ start, end int }

// claim is a span of a run to replace with placeholder.
type claim struct {
	span
	placeholder string
}

// run is one digit run: its groups and whether it starts with '+'.
type run struct {
	s      string
	plus   bool
	groups []span
}

func redactNumbers(s string) string {
	var b strings.Builder
	last, i := 0, 0
	for i < len(s) {
		c := s[i]
		startsRun := isDigit(c) || (c == '+' && i+1 < len(s) && isDigit(s[i+1]))
		if !startsRun {
			if isWordByte(c) {
				i = skipWord(s, i) // letters glued to digits: ord_000123, v1, 4bf92f...
			} else {
				i++
			}
			continue
		}
		if gluedBefore(s, i) {
			// Part of a word or a decimal: 1.5, x.0244123456, ord-000123.
			i = skipWord(s, i+1)
			continue
		}
		r, end := scanRun(s, i)
		i = end
		for _, cl := range r.claims() {
			b.WriteString(s[last:cl.start])
			b.WriteString(cl.placeholder)
			last = cl.end
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// gluedBefore reports whether the run starting at i is attached to what
// comes before it: a word character, a decimal point, or a '-' after a word
// character (an ID such as ord-000123).
func gluedBefore(s string, i int) bool {
	if i == 0 {
		return false
	}
	switch p := s[i-1]; {
	case isWordByte(p) || p == '.':
		return true
	case p == '-':
		return i >= 2 && isWordByte(s[i-2])
	}
	return false
}

// scanRun reads the digit run starting at i and returns it with the index
// just after it. A last group glued to a following word (1005ms, 30s) is not
// part of the run.
func scanRun(s string, i int) (run, int) {
	r := run{s: s, plus: s[i] == '+'}
	j := i
	if r.plus {
		j++
	}
	for {
		k := j
		for k < len(s) && isDigit(s[k]) {
			k++
		}
		r.groups = append(r.groups, span{j, k})
		if k+1 < len(s) && isSeparator(s[k]) && isDigit(s[k+1]) {
			j = k + 1
			continue
		}
		if k < len(s) && isWordByte(s[k]) {
			r.groups = r.groups[:len(r.groups)-1]
		}
		return r, k
	}
}

// claims returns what to replace in the run, in order.
func (r run) claims() []claim {
	if len(r.groups) == 0 {
		return nil
	}
	var out []claim
	taken := make([]bool, len(r.groups))
	if r.plus {
		if last := r.internationalPhoneEnd(); last >= 0 {
			out = append(out, claim{span{r.groups[0].start - 1, r.groups[last].end}, phone})
			for g := 0; g <= last; g++ {
				taken[g] = true
			}
		}
	}
	out = append(out, r.claimWindows(taken, card, 19, r.isCard)...)
	out = append(out, r.claimWindows(taken, phone, 11, r.isLocalPhone)...)
	slices.SortFunc(out, func(a, b claim) int { return a.start - b.start })
	return out
}

// internationalPhoneEnd returns the last group of the longest prefix of a
// '+' run that holds 7 to 15 digits (E.164 allows at most 15), or -1.
func (r run) internationalPhoneEnd() int {
	digits, last := 0, -1
	for g, sp := range r.groups {
		digits += sp.end - sp.start
		if digits > 15 {
			break
		}
		if digits >= 7 {
			last = g
		}
	}
	return last
}

// claimWindows scans the groups from left to right. At each free group it
// tries the longest window of free groups first, holding at most maxDigits
// digits, and claims the first window that ok accepts.
func (r run) claimWindows(taken []bool, placeholder string, maxDigits int, ok func(i, j int) bool) []claim {
	var out []claim
	for i := 0; i < len(r.groups); i++ {
		if taken[i] {
			continue
		}
		j, digits := i, 0
		for j < len(r.groups) && !taken[j] && digits+r.len(j) <= maxDigits {
			digits += r.len(j)
			j++
		}
		for j--; j >= i; j-- {
			if ok(i, j) {
				out = append(out, claim{span{r.groups[i].start, r.groups[j].end}, placeholder})
				for g := i; g <= j; g++ {
					taken[g] = true
				}
				i = j
				break
			}
		}
	}
	return out
}

// isCard reports whether groups i..j look like a payment card number:
// 13 to 19 digits that pass the Luhn check, starting with 2 to 9, either
// ungrouped or grouped as 4 digits then groups of 3 to 6 (4-4-4-4, 4-6-5),
// joined by spaces or dashes, not part of a decimal and not after a '+'.
func (r run) isCard(i, j int) bool {
	if (i == 0 && r.plus) || r.dotBefore(i) || r.dotAfter(j) {
		return false
	}
	if j > i && r.len(i) != 4 {
		return false
	}
	for g := i + 1; g <= j; g++ {
		if r.s[r.groups[g].start-1] == '.' || r.len(g) < 3 || r.len(g) > 6 {
			return false
		}
	}
	digits := r.digits(i, j)
	return len(digits) >= 13 && digits[0] >= '2' && luhn(digits)
}

// isLocalPhone reports whether groups i..j look like a phone number in
// national format: a trunk 0 then a non-zero digit, 10 or 11 digits in all,
// either ungrouped or in up to 4 groups of which the first has at least 3
// digits and the others at least 2, and not part of a decimal.
func (r run) isLocalPhone(i, j int) bool {
	if r.dotBefore(i) || r.dotAfter(j) || j-i >= 4 {
		return false
	}
	first := r.s[r.groups[i].start:r.groups[i].end]
	if len(first) < 3 || first[0] != '0' || first[1] == '0' {
		return false
	}
	for g := i + 1; g <= j; g++ {
		if r.len(g) < 2 {
			return false
		}
	}
	n := len(r.digits(i, j))
	return n == 10 || n == 11
}

func (r run) len(g int) int { return r.groups[g].end - r.groups[g].start }

// dotBefore reports whether group g follows a '.' separator, as the
// fraction of a decimal number does.
func (r run) dotBefore(g int) bool {
	return g > 0 && r.s[r.groups[g].start-1] == '.'
}

// dotAfter reports whether a '.' separator follows group g.
func (r run) dotAfter(g int) bool {
	return g < len(r.groups)-1 && r.s[r.groups[g].end] == '.'
}

// digits returns the digits of groups i..j without separators.
func (r run) digits(i, j int) string {
	var b strings.Builder
	for g := i; g <= j; g++ {
		b.WriteString(r.s[r.groups[g].start:r.groups[g].end])
	}
	return b.String()
}

// luhn reports whether the decimal digits pass the Luhn checksum.
func luhn(digits string) bool {
	sum, double := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isSeparator(c byte) bool { return c == ' ' || c == '-' || c == '.' }

func isWordByte(c byte) bool {
	return isDigit(c) || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func skipWord(s string, i int) int {
	for i < len(s) && isWordByte(s[i]) {
		i++
	}
	return i
}
