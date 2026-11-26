// Command wasitus builds an incident timeline from observability and change data
// and reports whether the evidence points at our own change or an external cause.
package main

import (
	"fmt"
	"io"
	"os"
	"time"

	_ "time/tzdata" // embed zone data so --config timezones work on any OS
)

// version is set at build time by the release tooling.
var version = "0.0.0-dev"

// Exit codes. They are part of the public interface: scripts depend on them.
const (
	exitOK      = 0
	exitError   = 1 // could not produce a result
	exitUsage   = 2 // bad command line
	exitPartial = 3 // produced a result, but some collectors failed
)

const usage = `wasitus: was it us? Incident timelines and verdicts.

Usage:
  wasitus timeline --from TIME [--to TIME] [--config FILE] [--timeout DURATION]
  wasitus version

TIME is "now", an offset into the past such as "-2h", an RFC 3339 timestamp,
or "YYYY-MM-DD HH:MM" read in the config timezone.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, time.Now))
}

// run is main without the process-level side effects, so tests can drive it.
func run(args []string, stdout, stderr io.Writer, now func() time.Time) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(stdout, "wasitus", version)
		return exitOK
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "timeline":
		return runTimeline(args[1:], stdout, stderr, now())
	default:
		fmt.Fprintf(stderr, "wasitus: unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}
