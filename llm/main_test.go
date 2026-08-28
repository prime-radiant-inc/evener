package llm

import (
	"os"
	"testing"
)

// TestMain loads the process-wide embedded registry before any test runs.
// A client built without WithRegistry resolves against it on its first
// dispatch, and parsing the catalog inside a test that budgets a few hundred
// milliseconds for the adapter call — generate_test.go's timeout tests, twice
// as slow again under -race — would make that budget flaky.
func TestMain(m *testing.M) {
	EmbeddedRegistry()
	os.Exit(m.Run())
}
