// Package golden compares test output with expected files in testdata.
// Run tests with UPDATE_GOLDEN=1 to write the current output as the new
// expectation, then review the change with git diff.
package golden

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Assert fails the test if got differs from the contents of path.
func Assert(t testing.TB, path string, got []byte) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden file (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s (run with UPDATE_GOLDEN=1 to accept)\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}
