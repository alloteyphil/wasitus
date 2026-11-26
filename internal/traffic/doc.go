// Package traffic decides whether request volume in an incident window was far
// above normal for that time of day: the evidence behind the "traffic" cause
// class (docs/ARCHITECTURE.md, section 4).
//
// Request and error counts arrive as fixed-size UTC buckets (5 minutes by
// default). Analyze compares the window with the median of the same time of
// day on each of the previous days (7 by default). ErrorMixStable checks
// whether the kinds of error stayed the same: more traffic with the same error
// mix points at the traffic itself, while new kinds of error point at another
// cause that happened to come with more traffic.
//
// Everything here is a pure function of its arguments: no I/O and no clock.
package traffic
