//go:build !unix

package schema

import "github.com/spf13/afero"

// lockSessionMetaCrossProcess has no cross-process lock on this platform, so only
// the in-process striped mutex serializes Revision updates.
func lockSessionMetaCrossProcess(_ afero.Fs, _, _ string) (func(), bool, error) {
	return func() {}, false, nil
}
