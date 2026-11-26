package collector

import (
	"context"
	"fmt"
	"sync"

	"github.com/alloteyphil/wasitus/internal/event"
)

// Collector reads events from one source. Implementations must be read-only and
// should stop work when ctx is cancelled. They may return events outside the
// window (the runner filters them) and may return partial results with an error.
type Collector interface {
	Name() string
	Collect(ctx context.Context, w event.Window) ([]event.Event, error)
}

// Failure records a problem with one collector. The run continues without it.
type Failure struct {
	Collector string
	Err       error
}

func (f Failure) Error() string { return f.Collector + ": " + f.Err.Error() }

// Result is the merged output of a run.
type Result struct {
	Events   []event.Event // normalised, validated, de-duplicated and sorted
	Failures []Failure     // in the same order as the collectors were given
}

// Run executes the collectors concurrently over w and merges their output into
// one timeline. A failing or panicking collector does not stop the others: its
// error is recorded and any events it did return are kept. Invalid events are
// dropped and reported; events outside w are dropped silently.
func Run(ctx context.Context, w event.Window, collectors []Collector) Result {
	type output struct {
		name   string
		events []event.Event
		err    error
	}
	outs := make([]output, len(collectors))

	var wg sync.WaitGroup
	for i, c := range collectors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[i].name = c.Name()
			outs[i].events, outs[i].err = collectSafely(ctx, w, c)
		}()
	}
	wg.Wait()

	var res Result
	for _, o := range outs {
		if o.err != nil {
			res.Failures = append(res.Failures, Failure{Collector: o.name, Err: o.err})
		}
		var invalid int
		var firstInvalid error
		for _, e := range o.events {
			e = e.Normalize()
			if err := e.Validate(); err != nil {
				invalid++
				if firstInvalid == nil {
					firstInvalid = err
				}
				continue
			}
			if w.Contains(e.Time) {
				res.Events = append(res.Events, e)
			}
		}
		if invalid > 0 {
			res.Failures = append(res.Failures, Failure{
				Collector: o.name,
				Err:       fmt.Errorf("dropped %d invalid event(s), first: %w", invalid, firstInvalid),
			})
		}
	}
	res.Events = event.Dedupe(res.Events)
	event.Sort(res.Events)
	return res
}

// collectSafely turns a collector panic into an error so one bad collector
// cannot crash an investigation.
func collectSafely(ctx context.Context, w event.Window, c Collector) (events []event.Event, err error) {
	defer func() {
		if r := recover(); r != nil {
			events, err = nil, fmt.Errorf("panic: %v", r)
		}
	}()
	return c.Collect(ctx, w)
}
