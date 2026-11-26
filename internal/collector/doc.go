// Package collector defines the read-only Collector interface and the runner that
// executes collectors concurrently, applies the lookback window and tolerates
// individual collector failures. Source implementations live in subpackages.
package collector
