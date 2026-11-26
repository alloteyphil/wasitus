// Package changesfile reads a YAML file of manual notes: things people did or
// noticed that no tool records, such as "02:30 restarted Redis" or "provider
// emailed about degraded payments".
//
// File format:
//
//	# changes.yaml: a list of entries
//	- time: "2026-11-14 02:30"    # RFC 3339, or "YYYY-MM-DD HH:MM" in the config timezone
//	  kind: change                # change (default) or external
//	  service: orders-service     # optional
//	  title: Restarted Redis
//	  severity: info              # info (default), warning or critical
//	  link: https://...           # optional
package changesfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/alloteyphil/wasitus/internal/event"
)

// Source is the value of Event.Source for events from this collector.
const Source = "changes-file"

type entry struct {
	Time     string `yaml:"time"`
	Kind     string `yaml:"kind"`
	Service  string `yaml:"service"`
	Title    string `yaml:"title"`
	Severity string `yaml:"severity"`
	Link     string `yaml:"link"`
}

// Collector reads events from a changes file.
type Collector struct {
	path string
	loc  *time.Location
}

// New returns a collector for the file at path. Times without a zone are read in loc.
func New(path string, loc *time.Location) *Collector {
	return &Collector{path: path, loc: loc}
}

// Name implements collector.Collector.
func (c *Collector) Name() string { return Source }

// Collect implements collector.Collector. It returns every valid entry; window
// filtering is left to the runner. Invalid entries are reported together in the
// error while the valid ones are still returned.
func (c *Collector) Collect(_ context.Context, _ event.Window) ([]event.Event, error) {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return nil, fmt.Errorf("read changes file: %w", err)
	}
	return parse(data, c.loc)
}

func parse(data []byte, loc *time.Location) ([]event.Event, error) {
	var entries []entry
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&entries); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil // empty file: nothing to report
		}
		return nil, fmt.Errorf("parse changes file: %w", err)
	}

	events := make([]event.Event, 0, len(entries))
	var errs []error
	for i, en := range entries {
		e, err := en.toEvent(loc)
		if err != nil {
			errs = append(errs, fmt.Errorf("entry %d: %w", i+1, err))
			continue
		}
		events = append(events, e)
	}
	return events, errors.Join(errs...)
}

func (en entry) toEvent(loc *time.Location) (event.Event, error) {
	t, err := event.ParseAbsoluteTime(en.Time, loc)
	if err != nil {
		return event.Event{}, err
	}
	title := strings.TrimSpace(en.Title)
	if title == "" {
		return event.Event{}, errors.New("missing title")
	}
	kind := event.Kind(en.Kind)
	if kind == "" {
		kind = event.KindChange
	}
	if kind != event.KindChange && kind != event.KindExternal {
		return event.Event{}, fmt.Errorf("kind must be change or external, got %q", en.Kind)
	}
	return event.Event{
		ID:       stableID(t, title),
		Time:     t,
		Kind:     kind,
		Source:   Source,
		Service:  strings.TrimSpace(en.Service),
		Title:    title,
		Severity: event.Severity(en.Severity), // empty becomes info; unknown values fail validation in the runner
		Link:     en.Link,
	}, nil
}

// stableID gives an entry the same ID on every run, so re-reading the file
// never creates duplicates.
func stableID(t time.Time, title string) string {
	sum := sha256.Sum256([]byte(t.UTC().Format(time.RFC3339Nano) + "\x00" + title))
	return hex.EncodeToString(sum[:6])
}
