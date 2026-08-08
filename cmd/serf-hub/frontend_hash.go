package main

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"sort"
)

// frontendDistHash computes a deterministic SHA256 hash of the entire
// embedded frontend distribution directory. It walks the FS in sorted order
// to ensure the hash is stable across builds. The hash is suitable for
// identifying which frontend build is embedded in a running serf-hub binary.
func frontendDistHash(distFS fs.FS) (string, error) {
	h := sha256.New()
	var paths []string

	// Walk the FS and collect all file paths in sorted order.
	err := fs.WalkDir(distFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	// Sort paths to ensure deterministic ordering.
	sort.Strings(paths)

	// Hash each file's path and content in order.
	for _, path := range paths {
		// Hash the path so renamed files produce different hashes.
		_, _ = fmt.Fprintf(h, "%s:", path)
		b, err := fs.ReadFile(distFS, path)
		if err != nil {
			return "", err
		}
		_, _ = h.Write(b)
	}

	return fmt.Sprintf("%x", h.Sum(nil))[:12], nil // Return first 12 hex chars
}
