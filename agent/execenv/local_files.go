package execenv

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/internal/tool/repair"
)

// fileTargetMode says which of fileTarget's plain-arm checks an operation
// takes. The sandboxed and granted-scratch arms are the same for every mode
// except that targetWrite also refuses a relative path under a dead sandbox
// root. The modes exist because the arms differ on purpose; file_op_arm_parity_test.go
// pins each operation's outcome on each arm.
type fileTargetMode int

const (
	// targetRead resolves the path as given. A read never checks that its
	// anchor is live, so a vanished root surfaces as the missing file.
	targetRead fileTargetMode = iota
	// targetReadRaw reads through the write path on the plain arm, so an empty
	// path is refused there.
	targetReadRaw
	// targetWrite requires a live anchor (the sandbox root, or the working
	// directory on the plain arm) and resolves through the write path.
	targetWrite
)

// fileTarget is the layer chosen for one path and the absolute path to act on.
// sfs is the sandboxed layer, the granted session-scratch layer, or nil for the
// plain filesystem. The caller must release it.
type fileTarget struct {
	sfs *sandboxFS
	abs string
}

func (t fileTarget) release() { t.sfs.release() }

// fileTarget picks the arm for path: the sandboxed layer when the file tools
// are confined, else the granted scratch layer when path lies in it, else the
// plain filesystem.
func (e *LocalExecutionEnvironment) fileTarget(tool, path string, mode fileTargetMode) (fileTarget, error) {
	if sfs := e.sandbox(); sfs != nil {
		if mode == targetWrite {
			if err := e.requireLiveSandboxRootForRelative(sfs, tool, path); err != nil {
				sfs.release()
				return fileTarget{}, err
			}
		}
		return fileTarget{sfs: sfs, abs: e.resolve(path)}, nil
	}
	abs := e.resolve(path)
	if sfs := e.scratchSandboxFor(abs); sfs != nil {
		return fileTarget{sfs: sfs, abs: abs}, nil
	}
	switch mode {
	case targetReadRaw:
		abs, err := e.resolveWrite(path)
		return fileTarget{abs: abs}, err
	case targetWrite:
		if err := e.requireLiveWorkingDirForRelative(path); err != nil {
			return fileTarget{}, err
		}
		abs, err := e.resolveWrite(path)
		return fileTarget{abs: abs}, err
	}
	return fileTarget{abs: abs}, nil
}

// ReadFile reads the file at path (resolved relative to RootDir) and returns
// its contents. Image and PDF files are returned as base64-encoded data with a
// descriptive header. Files containing a NUL byte are rejected as binary. For
// text files, lines are returned numbered, starting at offsetLine (default 1)
// and limited to limitLines lines (default 2000); CRLF line endings are
// normalized to LF.
func (e *LocalExecutionEnvironment) ReadFile(path string, offsetLine *int, limitLines *int) (string, error) {
	return e.ReadFileCtx(context.Background(), path, offsetLine, limitLines)
}

// ReadFileCtx is ReadFile with the caller's context threaded to the read, so a
// cancelled call ends a read parked on a stream instead of waiting it out.
func (e *LocalExecutionEnvironment) ReadFileCtx(ctx context.Context, path string, offsetLine *int, limitLines *int) (string, error) {
	out, _, err := e.ReadFileAndBytesCtx(ctx, path, offsetLine, limitLines)
	return out, err
}

// ReadFileAndBytesCtx is ReadFileCtx that also returns the whole file's bytes
// as this read loaded them, for a caller that must record exactly what the
// read saw. The bytes are the complete file even when offset and limit select
// only some of its lines; on error they are nil.
func (e *LocalExecutionEnvironment) ReadFileAndBytesCtx(ctx context.Context, path string, offsetLine *int, limitLines *int) (string, []byte, error) {
	t, err := e.fileTarget("read_file", path, targetRead)
	if err != nil {
		return "", nil, err
	}
	defer t.release()
	sfs, abs := t.sfs, t.abs
	var b []byte
	if sfs != nil {
		// Sandboxed or explicitly granted scratch: race-safe fd read
		// (symlink-refusing, root/denylist-checked).
		// The image/PDF/binary/line-numbering contract below is applied identically
		// to the returned bytes, so the output is unchanged from the off path.
		b, err = sfs.readFileCtx(ctx, "read_file", abs)
	} else {
		b, err = readFileCappedCtx(ctx, e.filesystem(), abs)
	}
	if err != nil {
		// Best-effort "did you mean" suggestion for a genuine missing-file miss
		// (never for a permission error or a directory-not-a-file error). Skipped
		// when sandboxed: the diagnostic listing walks plain os.Stat/os.ReadDir
		// outside the confined resolver, and could name a masked or denied
		// directory's contents that the sandbox would otherwise keep the model
		// from seeing. Off/unsandboxed is the default mode, so this still covers
		// the common case; sandboxed sessions just get the raw ENOENT.
		if sfs == nil && errors.Is(err, fs.ErrNotExist) {
			if suggestion := readFileNotFoundSuggestion(abs); suggestion != "" {
				return "", nil, fmt.Errorf("%w. %s", err, suggestion)
			}
		}
		return "", nil, err
	}
	// Image files: return base64-encoded data instead of erroring on binary.
	if format := detectImageFormat(path, b); format != "" {
		encoded := base64.StdEncoding.EncodeToString(b)
		return fmt.Sprintf("[image: %s, %d bytes, base64 data follows]\n%s", format, len(b), encoded), b, nil
	}
	// Document files (PDF): return base64-encoded data for vision/content pipeline.
	if format := detectDocumentFormat(path, b); format != "" {
		encoded := base64.StdEncoding.EncodeToString(b)
		return fmt.Sprintf("[document: %s, %d bytes, base64 data follows]\n%s", format, len(b), encoded), b, nil
	}
	// Basic binary detection.
	if bytes.IndexByte(b, 0) >= 0 {
		return "", nil, fmt.Errorf("binary file (NUL byte): %s", path)
	}
	return NumberLines(string(b), offsetLine, limitLines), b, nil
}

// eachLine visits the '\n'-separated segments of text in order — including
// the final (possibly empty) segment after a trailing newline, exactly the
// segments strings.Split(text, "\n") yields — without materializing them all:
// a line-heavy file at the whole-file read cap would otherwise hold ~16 bytes
// of slice header per line at once (issue #4556). The walk stops once visit
// returns true.
func eachLine(text string, visit func(line string) bool) {
	for line := range strings.SplitSeq(text, "\n") {
		if visit(line) {
			return
		}
	}
}

// NumberLines presents text as ReadFile presents a text file: CRLF
// normalized to LF, lines numbered from offsetLine (default 1), at most
// limitLines lines (default 2000), and "" past the end. The window is
// extracted by scanning segments rather than splitting the file, so a
// line-heavy file at the whole-file read cap does not allocate per line for
// every line in it (issue #4556).
func NumberLines(text string, offsetLine, limitLines *int) string {
	start := 1
	if offsetLine != nil && *offsetLine > 0 {
		start = *offsetLine
	}
	limit := 2000
	if limitLines != nil && *limitLines > 0 {
		limit = *limitLines
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	// Skip to line start. Running out of newlines first means start is past
	// the final segment.
	for i := 1; i < start; i++ {
		idx := strings.IndexByte(text, '\n')
		if idx < 0 {
			return ""
		}
		text = text[idx+1:]
	}
	remaining := limit
	var out strings.Builder
	eachLine(text, func(line string) bool {
		fmt.Fprintf(&out, "%4d\t%s\n", start, line)
		start++
		remaining--
		return remaining == 0
	})
	return out.String()
}

// readFileNotFoundSuggestion renders a "did you mean" hint for a read_file
// ENOENT on absPath, or "" if it can't find anything worth suggesting.
//
// It walks up absPath's ancestors (starting at its own parent) until it finds
// one that exists as a directory, then fuzzy-matches absPath's basename
// against that directory's direct children. This single algorithm covers two
// distinct misses with the same code path: an ordinary typo in a file that
// lives right next to where the model looked (the walk terminates
// immediately, since the parent already exists), and a "doubled path
// segment" miss like ".../worktrees/x/worktrees/x/file.go" where the real
// file is at ".../worktrees/x/file.go" (the walk climbs past the fictional
// nested worktrees/x before landing on a real directory whose listing
// contains an exact match).
func readFileNotFoundSuggestion(absPath string) string {
	dir := filepath.Dir(absPath)
	for {
		info, err := os.Stat(dir)
		if err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // walked to the filesystem root without finding a real directory
		}
		dir = parent
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	matches := repair.TopMatches(filepath.Base(absPath), names, 3)
	if len(matches) == 0 {
		return ""
	}
	full := make([]string, len(matches))
	for i, m := range matches {
		full[i] = filepath.Join(dir, m)
	}
	if len(full) == 1 {
		return fmt.Sprintf("Did you mean %s?", full[0])
	}
	return fmt.Sprintf("Did you mean one of: %s?", strings.Join(full, ", "))
}

// WriteFile writes content to the file at path, creating any missing parent
// directories within the active sandbox policy. It returns a
// human-readable summary of the bytes written.
func (e *LocalExecutionEnvironment) WriteFile(path string, content string) (string, error) {
	if err := e.writeFileAs("write_file", path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

// EditFile replaces occurrences of oldString with newString in the file at
// path, subject to the active sandbox policy. If oldString is not
// found exactly, a whitespace-normalized fuzzy match is attempted. Unless
// replaceAll is true, oldString must match exactly once. It returns a summary
// of the number of replacements made.
func (e *LocalExecutionEnvironment) EditFile(path string, oldString string, newString string, replaceAll bool) (string, error) {
	return e.EditFileWithCtx(context.Background(), path, oldString, newString, replaceAll, nil)
}

// EditFileCtx is EditFile with the caller's context threaded to its read, so a
// cancelled edit ends a read parked on a stream. It is the interface-visible
// entry of ContextualFileReader.
func (e *LocalExecutionEnvironment) EditFileCtx(ctx context.Context, path string, oldString string, newString string, replaceAll bool) (string, error) {
	return e.EditFileWithCtx(ctx, path, oldString, newString, replaceAll, nil)
}

// EditFileWithCtx is EditFileCtx, except that finish, when not nil, turns the
// edited bytes into the bytes written, inside the same read and write.
func (e *LocalExecutionEnvironment) EditFileWithCtx(ctx context.Context, path string, oldString string, newString string, replaceAll bool, finish func(edited []byte) []byte) (string, error) {
	t, err := e.fileTarget("edit_file", path, targetWrite)
	if err != nil {
		return "", err
	}
	defer t.release()
	var b []byte
	if t.sfs != nil {
		// Deny an edit in a non-writable location up front (read-only mode, outside
		// the writable roots, or a masked/git-protected surface) before reading, so
		// the model gets a clean write denial rather than a match/not-found result.
		if derr := t.sfs.checkWritable("edit_file", t.abs); derr != nil {
			return "", derr
		}
		b, err = t.sfs.readFileCtx(ctx, "edit_file", t.abs)
	} else {
		b, err = readFileCappedCtx(ctx, e.filesystem(), t.abs)
	}
	if err != nil {
		return "", err
	}
	s := string(b)
	fuzzyNote := ""
	if !strings.Contains(s, oldString) {
		// Fuzzy fallback: try whitespace-normalized matching.
		match := findFuzzyMatch(s, oldString)
		if match == "" {
			if near := nearestFileRegion(s, oldString); near != "" {
				return "", fmt.Errorf("old_string not found in %s\nnearest text in the file (copy it exactly — your old_string may be a partial line or omit a line):\n%s", path, near)
			}
			return "", fmt.Errorf("old_string not found in %s", path)
		}
		oldString = match
		fuzzyNote = " [NOTE: Matched with whitespace normalization]"
	}
	if !replaceAll && strings.Count(s, oldString) != 1 {
		return "", fmt.Errorf("old_string not unique in %s; use replace_all=true or provide a more specific old_string", path)
	}
	n := strings.Count(s, oldString)
	if replaceAll {
		s = strings.ReplaceAll(s, oldString, newString)
	} else {
		s = strings.Replace(s, oldString, newString, 1)
		n = 1
	}
	edited := []byte(s)
	if finish != nil {
		edited = finish(edited)
	}
	if t.sfs != nil {
		if werr := t.sfs.writeFile("edit_file", t.abs, edited, 0o644); werr != nil {
			return "", werr
		}
	} else if werr := afero.WriteFile(e.filesystem(), t.abs, edited, 0o644); werr != nil {
		return "", werr
	}
	plural := "s"
	if n == 1 {
		plural = ""
	}
	return fmt.Sprintf("edited %s: %d replacement%s%s", path, n, plural, fuzzyNote), nil
}

// detectImageFormat checks file extension and magic bytes to identify image files.
// Returns the format name (e.g. "png", "jpeg") or "" if not an image.
func detectImageFormat(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	imageExts := map[string]string{
		".png": "png", ".jpg": "jpeg", ".jpeg": "jpeg",
		".gif": "gif", ".webp": "webp", ".bmp": "bmp",
		".tif": "tiff", ".tiff": "tiff",
	}
	if format, ok := imageExts[ext]; ok {
		return format
	}
	// Check magic bytes for images without recognized extension.
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) {
		return "png"
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "jpeg"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "gif"
	}
	if len(data) >= 2 && string(data[:2]) == "BM" {
		return "bmp"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "webp"
	}
	if len(data) >= 4 && (string(data[:4]) == "II*\x00" || string(data[:4]) == "MM\x00*") {
		return "tiff"
	}
	return ""
}

// detectDocumentFormat checks file extension and magic bytes to identify document files
// that can be processed natively by the model (e.g. PDFs). Returns the format name or "".
func detectDocumentFormat(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".pdf" {
		return "pdf"
	}
	// Check magic bytes: PDF files start with %PDF-
	if len(data) >= 5 && string(data[:5]) == "%PDF-" {
		return "pdf"
	}
	return ""
}

// FileExists reports whether a file or directory exists at path (resolved
// relative to RootDir).
func (e *LocalExecutionEnvironment) FileExists(path string) bool {
	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		return sfs.exists("file_exists", e.resolve(path))
	}
	_, err := os.Stat(e.resolve(path))
	return err == nil
}
