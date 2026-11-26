package github

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/alloteyphil/wasitus/internal/event"
)

// Values of the change_type attribute. The vocabulary (deploy, release, merge,
// config, infra, docs, test) is shared with internal/correlate, which weighs
// changes by type; this collector emits every type except config.
const (
	changeDeploy  = "deploy"
	changeRelease = "release"
	changeMerge   = "merge"
	changeInfra   = "infra"
	changeDocs    = "docs"
	changeTest    = "test"
)

// stateUnknown is the deployment state used when a deployment has no status yet.
const stateUnknown = "unknown"

type user struct {
	Login string `json:"login"`
}

// login returns the user's login, or "" for a missing (deleted) user.
func (u *user) login() string {
	if u == nil {
		return ""
	}
	return clean(u.Login)
}

type pullRequest struct {
	Number         int        `json:"number"`
	Title          string     `json:"title"`
	HTMLURL        string     `json:"html_url"`
	UpdatedAt      time.Time  `json:"updated_at"`
	MergedAt       *time.Time `json:"merged_at"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	User           *user      `json:"user"`
}

type deployment struct {
	ID          int64     `json:"id"`
	SHA         string    `json:"sha"`
	Ref         string    `json:"ref"`
	Environment string    `json:"environment"`
	CreatedAt   time.Time `json:"created_at"`
}

type deploymentStatus struct {
	State     string `json:"state"`
	LogURL    string `json:"log_url"`
	TargetURL string `json:"target_url"`
}

type release struct {
	ID          int64      `json:"id"`
	TagName     string     `json:"tag_name"`
	HTMLURL     string     `json:"html_url"`
	Draft       bool       `json:"draft"`
	CreatedAt   time.Time  `json:"created_at"`
	PublishedAt *time.Time `json:"published_at"`
	Author      *user      `json:"author"`
}

// pullEvents converts one page of closed pull requests into events for those
// merged inside w. done reports that the page reached pull requests last
// updated before w starts: the list is sorted by update time, newest first, and
// a merge always updates its pull request, so no later page can hold a merge
// inside w.
func pullEvents(repo, service string, body []byte, w event.Window) (events []event.Event, done bool, err error) {
	var prs []pullRequest
	if err := json.Unmarshal(body, &prs); err != nil {
		return nil, false, fmt.Errorf("decode pull requests: %w", err)
	}
	for _, pr := range prs {
		if pr.UpdatedAt.Before(w.From) {
			done = true
		}
		if pr.MergedAt == nil || !w.Contains(*pr.MergedAt) {
			continue // closed without merging, or merged outside the window
		}
		events = append(events, pr.event(repo, service))
	}
	return events, done, nil
}

func (pr pullRequest) event(repo, service string) event.Event {
	n := strconv.Itoa(pr.Number)
	return event.Event{
		ID:       "github:" + repo + ":pr:" + n,
		Time:     pr.MergedAt.UTC(),
		Kind:     event.KindChange,
		Source:   Source,
		Service:  service,
		Title:    joinTitle("Merged PR #"+n, clean(pr.Title)),
		Severity: event.SeverityInfo,
		Link:     webLink(pr.HTMLURL),
		Attrs: attrs(
			"repo", repo,
			"number", n,
			"author", pr.User.login(),
			"sha", clean(pr.MergeCommitSHA),
			"change_type", changeType(pr.Title),
		),
	}
}

// deploymentsIn returns the deployments on one page that were created inside w.
// done reports that the page reached deployments created before w starts; the
// list is newest first, so later pages hold only older deployments.
func deploymentsIn(body []byte, w event.Window) (in []deployment, done bool, err error) {
	var all []deployment
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, false, fmt.Errorf("decode deployments: %w", err)
	}
	for _, d := range all {
		if d.CreatedAt.Before(w.From) {
			done = true
		}
		if w.Contains(d.CreatedAt) {
			in = append(in, d)
		}
	}
	return in, done, nil
}

// latestStatus decodes a deployment's statuses, newest first, and returns the
// first one. A deployment with no statuses yet gets the zero status.
func latestStatus(body []byte) (deploymentStatus, error) {
	var statuses []deploymentStatus
	if err := json.Unmarshal(body, &statuses); err != nil {
		return deploymentStatus{}, fmt.Errorf("decode deployment statuses: %w", err)
	}
	if len(statuses) == 0 {
		return deploymentStatus{}, nil
	}
	return statuses[0], nil
}

func (d deployment) event(repo, service string, s deploymentStatus) event.Event {
	id := strconv.FormatInt(d.ID, 10)
	ref, sha, env := clean(d.Ref), clean(d.SHA), clean(d.Environment)
	state := clean(s.State)
	if state == "" {
		state = stateUnknown
	}
	shown := ref
	if shown == "" {
		shown = shortSHA(sha)
	}
	link := webLink(s.LogURL)
	if link == "" {
		link = webLink(s.TargetURL)
	}
	return event.Event{
		ID:       "github:" + repo + ":deployment:" + id,
		Time:     d.CreatedAt.UTC(),
		Kind:     event.KindChange,
		Source:   Source,
		Service:  service,
		Title:    fmt.Sprintf("Deployed %s to %s (%s)", orUnknown(shown), orUnknown(env), state),
		Severity: event.SeverityInfo,
		Link:     link,
		Attrs: attrs(
			"repo", repo,
			"change_type", changeDeploy,
			"environment", env,
			"ref", ref,
			"sha", sha,
			"state", state,
		),
	}
}

// releaseEvents converts one page of releases into events for those published
// inside w; drafts are skipped. done reports that the page reached releases
// created before w starts. The list is sorted by creation time, newest first,
// so later pages hold only older releases. A release drafted before an earlier
// page and published inside w is the one case this can miss.
func releaseEvents(repo, service string, body []byte, w event.Window) (events []event.Event, done bool, err error) {
	var rels []release
	if err := json.Unmarshal(body, &rels); err != nil {
		return nil, false, fmt.Errorf("decode releases: %w", err)
	}
	for _, r := range rels {
		if r.CreatedAt.Before(w.From) {
			done = true
		}
		if r.Draft || r.PublishedAt == nil || !w.Contains(*r.PublishedAt) {
			continue
		}
		events = append(events, r.event(repo, service))
	}
	return events, done, nil
}

func (r release) event(repo, service string) event.Event {
	tag := clean(r.TagName)
	return event.Event{
		ID:       "github:" + repo + ":release:" + strconv.FormatInt(r.ID, 10),
		Time:     r.PublishedAt.UTC(),
		Kind:     event.KindChange,
		Source:   Source,
		Service:  service,
		Title:    "Released " + orUnknown(tag),
		Severity: event.SeverityInfo,
		Link:     webLink(r.HTMLURL),
		Attrs: attrs(
			"repo", repo,
			"change_type", changeRelease,
			"tag", tag,
			"author", r.Author.login(),
		),
	}
}

// changeType classifies a pull request by the conventional-commit type at the
// start of its title ("docs(readme)!: ..." has type docs). Titles without a
// recognised type are plain merges.
func changeType(title string) string {
	prefix, _, ok := strings.Cut(title, ":")
	if !ok {
		return changeMerge
	}
	prefix = strings.TrimSuffix(strings.TrimSpace(prefix), "!")
	if i := strings.IndexByte(prefix, '('); i >= 0 && strings.HasSuffix(prefix, ")") {
		prefix = prefix[:i]
	}
	switch strings.ToLower(prefix) {
	case "docs":
		return changeDocs
	case "test":
		return changeTest
	case "ci", "build":
		return changeInfra
	}
	return changeMerge
}

// clean makes outside text safe to show on one line: control characters
// (newlines, terminal escapes) become spaces and runs of space collapse.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// webLink returns s if it is an http or https URL, and "" otherwise, so a
// strange value from the API never becomes a link in a report.
func webLink(s string) string {
	if strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") {
		return clean(s)
	}
	return ""
}

func joinTitle(head, rest string) string {
	if rest == "" {
		return head
	}
	return head + ": " + rest
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func shortSHA(sha string) string {
	if r := []rune(sha); len(r) > 7 {
		return string(r[:7])
	}
	return sha
}

// attrs builds an attribute map from key, value pairs, leaving out empty values.
func attrs(kv ...string) map[string]string {
	m := make(map[string]string, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			m[kv[i]] = kv[i+1]
		}
	}
	return m
}
