// Package verdict combines error-fingerprint groups, traffic spike analysis,
// and correlated change candidates into a deterministic verdict: OUR_SIDE,
// EXTERNAL, or UNKNOWN, together with a confidence level and the evidence lines
// that produced it (see docs/adr/0004-deterministic-verdict.md).
//
// Precedence (with DefaultConfig):
//
//  1. Traffic surge (EXTERNAL / traffic): traffic is a confirmed spike against
//     baseline and the error mix is unchanged (including zero errors).
//  2. External error class (EXTERNAL / account | limit | expiry | dependency):
//     at least MinErrorSamples errors exist and one external class holds at
//     least DominanceShare (60%) of all errors — even if a recent deploy
//     happened (scenario S7).
//  3. Our side (OUR_SIDE / change): at least MinErrorSamples errors exist,
//     the code class holds at least DominanceShare (60%), and at least one
//     correlated change scores at least MinChangeScore (0.60).
//  4. Unknown (UNKNOWN): evidence is absent, below MinErrorSamples, split so
//     no class dominates (scenario S8), or code errors occurred with no
//     change scoring at least MinChangeScore. Top classes and change
//     candidates are returned for human inspection.
//
// Missing inputs (Input.Missing) are never read as "nothing found". If the
// window's error samples are missing, Decide returns UNKNOWN before any rule
// above runs. If traffic, baseline errors, or a change source are missing,
// the rules run on what was read, each gap is named in Evidence, and
// confidence is capped at medium.
//
// Decide is a pure function with no I/O or clock access; the same input always
// produces the same result and evidence lines.
package verdict
