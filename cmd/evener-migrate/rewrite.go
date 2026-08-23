package migrate

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"

	"primeradiant.com/evener/internal/legacypaths"
)

// maxRewriteFileSize caps how large a file rewriteLegacyPaths will read into
// memory to scan for legacy path references. Session transcripts and API
// logs can run into the hundreds of megabytes; skipping those is
// deliberate — they are historical records, not live config, and repair-on-
// rerun would otherwise re-read enormous files on every invocation for no
// behavioral gain.
const maxRewriteFileSize = 8 * 1024 * 1024 // 8 MiB

// binarySniffWindow is how many leading bytes rewriteLegacyPaths inspects
// for a NUL byte when deciding whether a file is binary.
const binarySniffWindow = 8000

// rewriteLegacyPaths walks dst and rewrites every occurrence of the old
// legacy root prefix to new, in text files only. It is meant to be called
// after a migration moves a root into place, and also when a re-run finds
// the destination already migrated — so a re-run repairs config/state files
// that still embed the pre-migration absolute path (e.g. a plugin
// marketplace registry's installLocation).
//
// It skips:
//   - directories that are themselves git working trees (contain a .git
//     entry, file or dir) — plugin marketplaces are git clones and any
//     legacy path they need repaired lives in the registry file beside them,
//     not inside the clone; rewriting tracked file content would silently
//     corrupt the checkout,
//   - symlinks, since rewriting would edit whatever they point at, which may
//     live outside the migrated tree,
//   - files over maxRewriteFileSize,
//   - files that sniff as binary (a NUL byte in the first
//     binarySniffWindow bytes, or content that isn't valid UTF-8).
//
// Rewrites are idempotent: a file with no remaining occurrences of old is
// left untouched and not reported. Each file actually changed is logged to
// stdout with the file path and the number of replacements made.
func rewriteLegacyPaths(dst, oldRoot, newRoot string, stdout io.Writer) error {
	return filepath.WalkDir(dst, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, gitErr := os.Lstat(filepath.Join(path, ".git")); gitErr == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxRewriteFileSize {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		if looksBinary(data) {
			return nil
		}

		rewritten, n := legacypaths.Rewrite(string(data), oldRoot, newRoot)
		if n == 0 {
			return nil
		}
		if err := os.WriteFile(path, []byte(rewritten), info.Mode().Perm()); err != nil {
			return fmt.Errorf("rewriting %s: %w", path, err)
		}
		_, _ = fmt.Fprintf(stdout, "rewrote %d path reference(s) in %s\n", n, path)
		return nil
	})
}

// looksBinary reports whether data should be treated as binary rather than
// text: a NUL byte within the first binarySniffWindow bytes, or content
// that isn't valid UTF-8.
func looksBinary(data []byte) bool {
	sniff := data
	if len(sniff) > binarySniffWindow {
		sniff = sniff[:binarySniffWindow]
	}
	return bytes.Contains(sniff, []byte{0}) || !utf8.Valid(data)
}
