package appsource

import (
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// requireWireError asserts err carries an appwire wire error of the wanted
// code, failing the test when it does not, and returns the error for
// follow-up assertions on its other fields.
func requireWireError(t *testing.T, err error, want int) appwire.WireError {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != want {
		t.Fatalf("error = %v, want wire error code %d", err, want)
	}
	return wire
}
