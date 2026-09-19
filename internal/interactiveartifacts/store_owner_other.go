//go:build !unix

package interactiveartifacts

import "os"

// Store ownership qualification requires Unix file ownership semantics.
func fileOwner(os.FileInfo) (int, bool) { return 0, false }
