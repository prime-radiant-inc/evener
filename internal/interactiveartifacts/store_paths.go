package interactiveartifacts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PrepareStoreDirectory creates a private directory through trusted traversal
// and returns its canonical path. Store, backups and the service owner lock must
// use this returned root. Root and the current UID are trusted; ordinary Unix
// ownership/mode semantics apply. This does not isolate malicious same-UID/root
// actors or account for access grants outside those semantics (such as ACLs).
func PrepareStoreDirectory(path string) (string, error) {
	canonical, _, err := prepareStoreDirectory(path)
	return canonical, err
}

func prepareStoreDirectory(path string) (string, []string, error) {
	absolute := path
	if !filepath.IsAbs(absolute) {
		working, err := os.Getwd()
		if err != nil {
			return "", nil, err
		}
		absolute = working + string(filepath.Separator) + path
	}
	absolute = strings.TrimRight(absolute, string(filepath.Separator))
	if absolute == "" {
		absolute = string(filepath.Separator)
	}
	// An ancestor alias may be trusted, but the private leaf itself is not an alias.
	if info, err := os.Lstat(absolute); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", nil, errors.New("artifact private directory must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", nil, err
	}
	created := make([]string, 0, 1)
	canonical, err := trustedDirectory(absolute, true, 0, &created)
	if err != nil {
		return "", nil, err
	}
	directory, err := os.Open(canonical)
	if err != nil {
		return "", nil, err
	}
	info, statErr := directory.Stat()
	closeErr := directory.Close()
	if err := errors.Join(statErr, closeErr); err != nil {
		return "", nil, err
	}
	if err := requirePrivateInfo(info, true); err != nil {
		return "", nil, err
	}
	return canonical, created, nil
}

// Every alias is checked at its original parent before resolving its target.
// The target is traversed by the same checks, preventing a protected alias from
// hiding a replaceable ancestor. Only verified directories can receive mkdir.
func trustedDirectory(absolute string, create bool, links int, created *[]string) (string, error) {
	current := string(filepath.Separator)
	info, err := os.Lstat(current)
	if err != nil {
		return "", err
	}
	if err := requireTrustedDirectory(info); err != nil {
		return "", fmt.Errorf("artifact traversal %s: %w", current, err)
	}
	for component := range strings.SplitSeq(strings.TrimPrefix(absolute, current), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next := filepath.Join(current, component)
		child, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := os.Mkdir(next, 0700); err == nil {
				*created = append(*created, next)
			} else if !errors.Is(err, os.ErrExist) {
				return "", err
			}
			child, err = os.Lstat(next)
		}
		if err != nil {
			return "", err
		}
		// A writable sticky parent protects entries only when their owners are
		// trusted, and the parent itself must also have a trusted owner.
		if !trustedOwner(child) {
			return "", fmt.Errorf("artifact traversal %s has an untrusted owner", next)
		}
		if child.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return "", errors.New("artifact traversal has too many symlinks")
			}
			target, err := os.Readlink(next)
			if err != nil {
				return "", err
			}
			if !filepath.IsAbs(target) {
				target = current + string(filepath.Separator) + target
			}
			current, err = trustedDirectory(target, false, links, created)
			if err != nil {
				return "", err
			}
		} else {
			if err := requireTrustedDirectory(child); err != nil {
				return "", fmt.Errorf("artifact traversal %s: %w", next, err)
			}
			current = next
		}
	}
	return current, nil
}

func trustedOwner(info os.FileInfo) bool {
	uid, ok := fileOwner(info)
	return ok && (uid == 0 || uid == os.Geteuid())
}
func requireTrustedDirectory(info os.FileInfo) error {
	if !info.IsDir() || !trustedOwner(info) || (info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
		return errors.New("directory must have a trusted owner and prevent other users replacing entries")
	}
	return nil
}
func requirePrivateInfo(info os.FileInfo, directory bool) error {
	uid, ok := fileOwner(info)
	mode := os.FileMode(0600)
	if directory {
		mode = 0700
	}
	if !ok || uid != os.Geteuid() || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) || info.Mode().Perm() != mode {
		return errors.New("artifact path must be service owned with private permissions")
	}
	return nil
}
func requirePrivatePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("artifact private path must not be a symlink")
	}
	return requirePrivateInfo(info, directory)
}
