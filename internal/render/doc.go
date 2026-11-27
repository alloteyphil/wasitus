// Package render turns an incident timeline, error groups, and verdict into
// terminal table, Markdown, or JSON output. Free-text fields pass through an
// optional redact.Redactor before rendering, without modifying the caller's
// data. Renderers are covered by golden-file tests.
package render
