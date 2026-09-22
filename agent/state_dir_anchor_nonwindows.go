//go:build !windows

package agent

import (
	"os"
	"path/filepath"
)

// anchorRelativeStateDir anchors a relative --state-dir to the process
// working directory WITHOUT cleaning, because the component walk that
// follows needs every component raw.
//
// A relative EvalSymlinks result stays relative, and anchoring it
// afterward — or anchoring through filepath.Abs — can preserve a lexical
// symlinked working directory, because os.Getwd prefers PWD whenever it
// matches ".", so a shell that cd'd through a symlink hands us the
// lexical form. The component walk resolves every existing component of
// the anchored path, lexical or not, so anchoring through the raw cwd
// loses nothing to the walk.
//
// filepath.Abs would also Clean ".." lexically, which is wrong across a
// symlink: the kernel resolves `link/../state` against the physical
// parent of link's target, while Clean folds it to link's lexical parent.
// Concatenation keeps every component raw so the walk can pop ".."
// against the resolved parent.
//
// On Getwd failure no faithful anchor is available; the caller falls
// back to the cleaning Abs.
func anchorRelativeStateDir(dir string) (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	return cwd + string(filepath.Separator) + dir, true
}
