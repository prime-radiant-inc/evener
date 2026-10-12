package hub

import "primeradiant.com/evener/appwire"

// causedWireError is a wire refusal that keeps the hub error it reports. The
// RPC layer resolves the WireError through errors.AsType and answers with it
// unchanged, while the hub, and its tests, can still match the cause with
// errors.Is or errors.As; appliedError has the same shape.
type causedWireError struct {
	wire  appwire.WireError
	cause error
}

func (e causedWireError) Error() string   { return e.wire.Message }
func (e causedWireError) Unwrap() []error { return []error{e.wire, e.cause} }

// invalidParamsFor is appwire.InvalidParams with cause's text, keeping cause
// in the error chain.
func invalidParamsFor(cause error) error {
	return causedWireError{wire: appwire.InvalidParams(cause.Error()), cause: cause}
}
