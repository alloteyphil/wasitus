package fingerprint

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// rule is one compiled rule. It matches when every all-condition holds and,
// if there are any-conditions, at least one of them holds too.
type rule struct {
	id     string
	class  Class
	all    []condition
	any    []condition
	source string // "user rules" or "built-in rules", for error messages
	line   int
}

func (r *rule) matches(in *input) bool {
	for i := range r.all {
		if !r.all[i].matches(in) {
			return false
		}
	}
	if len(r.any) == 0 {
		return true
	}
	for i := range r.any {
		if r.any[i].matches(in) {
			return true
		}
	}
	return false
}

// condition is one list entry of a match. Every key that is set must hold.
// Text to search for is stored lower-case; "" and 0 mean "not set".
type condition struct {
	messageContains       string
	exceptionType         string // exact, case-sensitive
	exceptionTypeContains string
	serverAddressContains string
	httpStatus            int
	outbound              *bool
	thirdParty            *bool
	attrEquals            map[string]string
}

func (c *condition) matches(in *input) bool {
	switch {
	case c.messageContains != "" && !strings.Contains(in.message, c.messageContains),
		c.exceptionType != "" && in.s.ExceptionType != c.exceptionType,
		c.exceptionTypeContains != "" && !strings.Contains(in.exceptionType, c.exceptionTypeContains),
		c.serverAddressContains != "" && !strings.Contains(in.serverAddress, c.serverAddressContains),
		c.httpStatus != 0 && in.s.HTTPStatus != c.httpStatus,
		c.outbound != nil && in.s.Outbound != *c.outbound,
		c.thirdParty != nil && in.thirdParty != *c.thirdParty:
		return false
	}
	for k, want := range c.attrEquals {
		if got, ok := in.s.Attrs[k]; !ok || got != want {
			return false
		}
	}
	return true
}

// The YAML shapes. Each one checks its own keys, so a typo is an error that
// points at its line instead of a condition silently left out.

var (
	ruleKeys      = []string{"id", "class", "match"}
	matchKeys     = []string{"any", "all"}
	conditionKeys = []string{
		"message_contains", "exception_type", "exception_type_contains", "http_status",
		"outbound", "third_party", "server_address_contains", "attr_equals",
	}
	idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

type rawRule struct {
	ID    string   `yaml:"id"`
	Class string   `yaml:"class"`
	Match rawMatch `yaml:"match"`
}

func (r *rawRule) UnmarshalYAML(n *yaml.Node) error {
	if err := onlyKeys(n, "a rule", ruleKeys); err != nil {
		return err
	}
	type plain rawRule
	return n.Decode((*plain)(r))
}

type rawMatch struct {
	Any []rawCondition `yaml:"any"`
	All []rawCondition `yaml:"all"`
}

func (m *rawMatch) UnmarshalYAML(n *yaml.Node) error {
	if err := onlyKeys(n, "match", matchKeys); err != nil {
		return err
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		list, v := n.Content[i].Value, resolve(n.Content[i+1])
		if v.Kind != yaml.SequenceNode {
			return fmt.Errorf("line %d: %s must be a list of conditions", v.Line, list)
		}
		// Checked here because the decoder skips null entries without a word.
		for j, entry := range v.Content {
			if e := resolve(entry); e.Kind != yaml.MappingNode {
				return fmt.Errorf("%s entry %d (line %d): a condition must be a mapping, such as {http_status: 503}", list, j+1, e.Line)
			}
		}
	}
	type plain rawMatch
	return n.Decode((*plain)(m))
}

type rawCondition struct {
	MessageContains       *string           `yaml:"message_contains"`
	ExceptionType         *string           `yaml:"exception_type"`
	ExceptionTypeContains *string           `yaml:"exception_type_contains"`
	HTTPStatus            *int              `yaml:"http_status"`
	Outbound              *bool             `yaml:"outbound"`
	ThirdParty            *bool             `yaml:"third_party"`
	ServerAddressContains *string           `yaml:"server_address_contains"`
	AttrEquals            map[string]string `yaml:"attr_equals"`

	line int
}

func (c *rawCondition) UnmarshalYAML(n *yaml.Node) error {
	if err := onlyKeys(n, "a condition", conditionKeys); err != nil {
		return err
	}
	type plain rawCondition
	if err := n.Decode((*plain)(c)); err != nil {
		return err
	}
	c.line = n.Line
	return nil
}

// onlyKeys checks that n is a mapping whose keys are all in known and whose
// values are not null.
func onlyKeys(n *yaml.Node, what string, known []string) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: %s must be a mapping", n.Line, what)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode || !slices.Contains(known, k.Value) {
			return fmt.Errorf("line %d: unknown key %q in %s (known: %s)", k.Line, k.Value, what, strings.Join(known, ", "))
		}
		if v.ShortTag() == "!!null" {
			return fmt.Errorf("line %d: %s has no value", v.Line, k.Value)
		}
	}
	return nil
}

// resolve follows aliases to the node they point at.
func resolve(n *yaml.Node) *yaml.Node {
	for range 100 { // aliases cannot form cycles; this only bounds the loop
		if n.Kind != yaml.AliasNode || n.Alias == nil {
			break
		}
		n = n.Alias
	}
	return n
}

// parseRules decodes and compiles a YAML list of rules. Every bad rule is
// reported, each prefixed with source and the rule's id or position.
func parseRules(data []byte, source string) ([]rule, error) {
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil // empty or only comments
		}
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
		return nil, fmt.Errorf("%s: line %d: only one YAML document is allowed", source, next.Line)
	}

	list := &doc
	if list.Kind == yaml.DocumentNode {
		if len(list.Content) == 0 {
			return nil, nil
		}
		list = list.Content[0]
	}
	list = resolve(list)
	if list.ShortTag() == "!!null" {
		return nil, nil
	}
	if list.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: line %d: rules must be a list", source, list.Line)
	}

	rules := make([]rule, 0, len(list.Content))
	var errs []error
	for i, n := range list.Content {
		r, err := decodeRule(n)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %s: %w", source, ruleLabel(i, n), err))
			continue
		}
		r.source = source
		rules = append(rules, r)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return rules, nil
}

// ruleLabel names a rule in an error: by id when it has one, else by position.
func ruleLabel(i int, n *yaml.Node) string {
	n = resolve(n)
	if n.Kind == yaml.MappingNode {
		for j := 0; j+1 < len(n.Content); j += 2 {
			k, v := n.Content[j], resolve(n.Content[j+1])
			if k.Value == "id" && v.Kind == yaml.ScalarNode && v.ShortTag() != "!!null" && v.Value != "" {
				return fmt.Sprintf("rule %q (line %d)", v.Value, n.Line)
			}
		}
	}
	return fmt.Sprintf("rule %d (line %d)", i+1, n.Line)
}

func decodeRule(n *yaml.Node) (rule, error) {
	if resolve(n).Kind != yaml.MappingNode {
		// Checked here because the decoder turns a null rule into an empty one.
		return rule{}, errors.New("a rule must be a mapping with id, class and match")
	}
	var raw rawRule
	if err := n.Decode(&raw); err != nil {
		return rule{}, err
	}
	r := rule{id: raw.ID, class: Class(raw.Class), line: resolve(n).Line}
	switch {
	case raw.ID == "":
		return rule{}, errors.New("missing id")
	case !idPattern.MatchString(raw.ID):
		return rule{}, fmt.Errorf("id %q may use only letters, digits, '.', '_' and '-'", raw.ID)
	case raw.ID == FallbackOutbound || raw.ID == FallbackOurs:
		return rule{}, fmt.Errorf("id %q is reserved for the fallback", raw.ID)
	case raw.Class == "":
		return rule{}, errors.New("missing class")
	case raw.Class == "traffic":
		return rule{}, errors.New(`class "traffic" cannot come from a rule: traffic spikes are found from request rates`)
	case !r.class.Valid():
		return rule{}, fmt.Errorf("unknown class %q (known: code, dependency, account, limit, expiry)", raw.Class)
	case len(raw.Match.All)+len(raw.Match.Any) == 0:
		return rule{}, errors.New("match is empty: give it an any: or all: list of conditions")
	}

	var err error
	if r.all, err = compileConditions("all", raw.Match.All); err != nil {
		return rule{}, err
	}
	if r.any, err = compileConditions("any", raw.Match.Any); err != nil {
		return rule{}, err
	}
	return r, nil
}

func compileConditions(list string, raws []rawCondition) ([]condition, error) {
	out := make([]condition, 0, len(raws))
	for i := range raws {
		c, err := compileCondition(&raws[i])
		if err != nil {
			return nil, fmt.Errorf("%s entry %d (line %d): %w", list, i+1, raws[i].line, err)
		}
		out = append(out, c)
	}
	return out, nil
}

func compileCondition(rc *rawCondition) (condition, error) {
	var c condition
	set := 0
	text := func(key string, v *string, lower bool) (string, error) {
		if v == nil {
			return "", nil
		}
		set++
		if strings.TrimSpace(*v) == "" {
			return "", fmt.Errorf("%s is empty", key)
		}
		if lower {
			return strings.ToLower(*v), nil
		}
		return *v, nil
	}

	var err error
	if c.messageContains, err = text("message_contains", rc.MessageContains, true); err != nil {
		return c, err
	}
	if c.exceptionType, err = text("exception_type", rc.ExceptionType, false); err != nil {
		return c, err
	}
	if c.exceptionTypeContains, err = text("exception_type_contains", rc.ExceptionTypeContains, true); err != nil {
		return c, err
	}
	if c.serverAddressContains, err = text("server_address_contains", rc.ServerAddressContains, true); err != nil {
		return c, err
	}
	if rc.HTTPStatus != nil {
		set++
		if *rc.HTTPStatus < 100 || *rc.HTTPStatus > 599 {
			return c, fmt.Errorf("http_status %d is not an HTTP status (100-599)", *rc.HTTPStatus)
		}
		c.httpStatus = *rc.HTTPStatus
	}
	if rc.Outbound != nil {
		set++
		c.outbound = rc.Outbound
	}
	if rc.ThirdParty != nil {
		set++
		c.thirdParty = rc.ThirdParty
	}
	if rc.AttrEquals != nil {
		set++
		if len(rc.AttrEquals) == 0 {
			return c, errors.New("attr_equals is empty")
		}
		for k := range rc.AttrEquals {
			if strings.TrimSpace(k) == "" {
				return c, errors.New("attr_equals has an empty attribute name")
			}
		}
		c.attrEquals = rc.AttrEquals
	}
	if set == 0 {
		return c, errors.New("empty condition: it would match every error")
	}
	return c, nil
}
