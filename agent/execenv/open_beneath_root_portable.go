package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type openRegularBeneathRootFunc func(path, root string) (*os.File, error)

// openRegularBeneathRootNoFollowPortable contains the portable fallback's
// root policy behind an injected opener so its deterministic checks can be
// exercised directly on platforms that use the descriptor-relative walk.
// These checks provide deterministic refusal only: the root or first component
// can still be swapped after Lstat and before openFile follows the path.
func openRegularBeneathRootNoFollowPortable(path, root string, openFile openRegularBeneathRootFunc) (*os.File, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, &os.PathError{Op: "open root without symlinks", Path: root, Err: ErrNonTraversableRoot}
	}

	component, hasComponent, err := firstDirectoryComponentBeneathRoot(path, root)
	if err != nil {
		return nil, err
	}
	if hasComponent {
		info, err = os.Lstat(component)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, &os.PathError{
				Op:   "open root component without symlinks",
				Path: component,
				Err:  ErrNonTraversableRoot,
			}
		}
	}
	return openFile(path, root)
}

func firstDirectoryComponentBeneathRoot(path, root string) (string, bool, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", false, fmt.Errorf("open %q beneath %q: %w", path, root, err)
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return "", false, fmt.Errorf("open %q: path is the root directory, not a file", path)
	}
	if strings.HasPrefix(rel, "..") {
		return "", false, fmt.Errorf("open %q: path escapes root %q", path, root)
	}
	components := strings.Split(rel, string(filepath.Separator))
	if len(components) < 2 {
		return "", false, nil
	}
	return filepath.Join(root, components[0]), true, nil
}
