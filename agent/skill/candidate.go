package skill

import (
	"os"
	"path/filepath"
)

// skillDirectory accepts real directories and immediate-child directory links.
// Resolve only to classify the child, keeping its lexical path for all reads.
func skillDirectory(root string, entry os.DirEntry) (bool, error) {
	if entry.IsDir() {
		return true, nil
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false, nil
	}
	info, err := os.Stat(filepath.Join(root, entry.Name()))
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}
