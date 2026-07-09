//go:build !darwin

package sandbox

import "errors"

// errSeatbeltUnavailable is returned by the non-darwin seatbeltWrap stub. It is
// only ever hit if a seatbelt-backend policy reaches the kernel wrapper on a
// non-macOS host — an impossible state, because the resolver's fail-closed floor
// selects the seatbelt backend only when runtime.GOOS == "darwin". The stub
// exists so package sandbox compiles everywhere without local.go needing a build
// tag; Wrap treats a non-nil error here as a fail-closed invariant violation.
var errSeatbeltUnavailable = errors.New("sandbox: the seatbelt backend is only available on macOS")

// seatbeltWrap is the non-darwin stub: it refuses rather than returning an
// unconfined argv, so a seatbelt policy can never silently run without
// confinement on a host that cannot enforce it.
func seatbeltWrap(_ []string, _ ResolvedPolicy, _, _ string) ([]string, error) {
	return nil, errSeatbeltUnavailable
}
