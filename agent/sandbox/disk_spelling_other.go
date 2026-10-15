//go:build !darwin

package sandbox

import (
	"os"
	"path/filepath"
)

// diskSpelling returns the existing directory p as the filesystem stores it:
// p itself, provided every component is a real directory and none a symlink
// (these filesystems spell a name one way). A path that runs through a link is
// not the directory it names, so a link planted at a reported path cannot pass
// for the directory it points to.
func diskSpelling(p string) (string, bool) {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) {
		return "", false
	}
	for q := p; ; q = filepath.Dir(q) {
		fi, err := os.Lstat(q)
		if err != nil || !fi.IsDir() {
			return "", false
		}
		if filepath.Dir(q) == q {
			return p, true
		}
	}
}
