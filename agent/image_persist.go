package agent

// Durable persistence for user-input image attachments. Pasted images
// reach the model as inline ContentImage parts, but until now the bytes
// existed nowhere the model (or a later session) could reach them again:
// once context folding drops the image part, the image was gone. These
// helpers write each accepted attachment into the session's state
// directory so the recorded user message can name a stable, re-readable
// path.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
)

// attachmentsSubdir is the per-session directory, under
// <stateDir>/sessions/<id>/, where persisted input attachments live.
const attachmentsSubdir = "attachments"

// attachmentPathPrefixLen is how many hex characters of the attachment's
// sha256 prefix the stored filename carries. Combined with the sanitized
// original name it makes the path unique per content, stable across
// retries, and deduplicating for repeated pastes of the same bytes.
const attachmentPathPrefixLen = 16

// persistInputImages writes each image's bytes under
// <stateDir>/sessions/<id>/attachments/ and returns copies of the
// attachments carrying their on-disk Path. Sessions without a state
// directory (library/test shape) get their input back untouched, and a
// write failure degrades the same way: the image still rides the turn
// inline and its Path stays empty, so nothing is announced that a reader
// could not fetch. Each failure is reported as a warning on the general
// channel — firing the Notification hook like every other session file
// I/O failure — instead of failing the turn. A stored path the session's own
// file tools cannot read (a restricted sandbox keeps them inside the
// worktree while the state dir lives outside it) also stays unannounced: the
// file is still written for unrestricted readers, but the note promising a
// read_file must only name a path the model can actually fetch.
func (s *Session) persistInputImages(images []ImageAttachment) []ImageAttachment {
	if s == nil || len(images) == 0 || s.stateDir == "" {
		return images
	}
	dir := filepath.Join(s.stateDir, sessionsSubdir, s.id, attachmentsSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("persist input attachments: %v", err)})
		return images
	}
	// MkdirAll's Stat follows a symlink planted at the final component, so
	// a link there would succeed and be written through — and the mode
	// tightening below would reach the link's target. The store's directory
	// leaf gets the same no-follow treatment as the attachment files: what
	// sits at the path must be a real directory, not a link to one.
	if info, err := os.Lstat(dir); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("persist input attachments: %v", err)})
		return images
	} else if !info.IsDir() {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("persist input attachments: %q is not a directory", dir)})
		return images
	}
	// MkdirAll's mode applies only at creation: an attachments directory
	// that already exists (a buggy predecessor, a restore) keeps whatever
	// mode it landed with, and the attachments are private to the session's
	// user. A mode that cannot be enforced is a write failure, not a
	// best-effort shrug.
	if err := os.Chmod(dir, 0o700); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("persist input attachments: %v", err)})
		return images
	}
	out := slices.Clone(images)
	// The note promises read_file; a session whose registry does not carry the
	// tool (role toolsets are plugin-configurable) must not hear that promise.
	// The stored bytes are for later readers regardless.
	announceStoredPath := s.reg.Get("read_file") != nil
	for i := range out {
		if out[i].Path != "" {
			continue // already persisted by an earlier build of the same input
		}
		name := sanitizeAttachmentName(out[i])
		sum := sha256.Sum256(out[i].Data)
		path := filepath.Join(dir, hex.EncodeToString(sum[:attachmentPathPrefixLen/2])+"-"+name)
		if err := writeAttachmentFile(path, out[i].Data, writeAttachmentContents, fsyncAttachmentDir); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("persist input attachment %q: %v", name, err)})
			continue
		}
		if !announceStoredPath || !s.fileToolsCanRead(path) {
			continue
		}
		out[i].Path = path
	}
	return out
}

// writeAttachmentContents is the production content-write step; the parameter
// exists so tests can inject its failure without a process-global seam racing
// parallel tests.
func writeAttachmentContents(f *os.File, data []byte) error {
	_, err := f.Write(data)
	return err
}

// writeAttachmentFile stores data at the content-addressed path without
// following a symlink at the leaf or overwriting an existing entry: the
// O_CREAT|O_EXCL create is atomic against the directory entry, and when the
// path already holds anything — a file, a symlink pointing anywhere — the
// open fails with EEXIST without ever dereferencing it, so a planted symlink
// is refused, not written through. An existing entry is the same attachment
// from an earlier paste exactly when it is a regular file whose bytes match,
// which dedupes to a no-op; anything else there — a planted file, a symlink,
// a non-regular entry — is a write failure the caller reports like any
// other. A failed content write removes the partial file so the
// content-addressed name is never poisoned for a retry. Both success paths
// flush the directory entries that make the file reachable by name — its
// directory and that directory's parent, which held the directory's own
// first creation — before the caller can record the path; the dedupe path
// flushing too means a retry after a failed flush completes it rather than
// masking it.
func writeAttachmentFile(path string, data []byte, write func(*os.File, []byte) error, syncDir func(string) error) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		// One descriptor, validated before any read: a symlink at the leaf is
		// refused without its target ever being opened, a FIFO never blocks
		// the open, and the bytes are read from the same regular file that was
		// validated — nothing can be swapped between the check and the read.
		existing, rerr := readAttachmentForDedupe(path)
		if rerr != nil {
			return rerr
		}
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("existing file at %q holds different content", path)
		}
		return syncAttachmentDirs(path, syncDir)
	}
	if err := write(f, data); err != nil {
		_ = f.Close()
		_ = os.Remove(path) // a partial file must not poison the content-addressed name for retries
		return err
	}
	// The file must be on disk before the note naming it can be recorded in
	// the transcript: a crash right after the turn is persisted would otherwise
	// leave the model holding a promised path whose bytes never landed.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return syncAttachmentDirs(path, syncDir)
}

// syncAttachmentDirs flushes the directory entries that make a stored
// attachment reachable by name: the directory holding the file, and that
// directory's parent, which held the directory's own first creation (the
// persist path creates the attachments directory itself; the session
// directory above it belongs to the session's own construction, the
// transcript writer's contract). Both paths flush — a fresh create and a
// dedupe hit — because a create whose dir sync failed leaves the file in
// place (the jobs-store posture) and the retry must complete the flush
// instead of masking it. A filesystem that cannot sync a directory at all
// reports the same unsupported-sync errors the client-mutation store
// tolerates; the file's own contents are already flushed there.
func syncAttachmentDirs(path string, syncDir func(string) error) error {
	for _, dir := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
		if err := syncDir(dir); err != nil && !clientMutationSyncUnsupported(err) {
			return err
		}
	}
	return nil
}

// readAttachmentForDedupe reads the existing entry at the content-addressed
// path through the execenv no-follow open, so the dedupe compare never
// follows a planted symlink (even a same-bytes one) and never blocks opening
// a non-regular entry.
func readAttachmentForDedupe(path string) ([]byte, error) {
	f, err := execenv.OpenRegularNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only handle; close error is immaterial
	return io.ReadAll(f)
}

// fsyncAttachmentDir flushes the directory entry naming a just-created
// attachment, so a crash cannot leave the transcript holding a promised path
// whose file never became reachable by name. Same write-then-fsync-the-
// directory shape the jobs output store and the sandbox retention writer
// use for their durable files.
func fsyncAttachmentDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open attachments dir for sync: %w", err)
	}
	defer func() { _ = handle.Close() }()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync attachments dir: %w", err)
	}
	return nil
}

// fileToolsCanRead reports whether the session's in-process file tools — the
// layer read_file runs under — may read path, so the attachment note only
// promises what a tool call can fetch. An unconfined session (no sandbox
// policy, or an environment other than the local one) reads with plain os.
// persistInputImages always runs without s.mu held, so taking the lock here
// via currentEnv is safe.
func (s *Session) fileToolsCanRead(path string) bool {
	le, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok || le.Sandbox == nil {
		return true
	}
	return le.Sandbox.FileToolCanRead(path)
}

// sanitizeAttachmentName derives the stored filename for an attachment:
// the original name's stem, restricted to filesystem-safe ASCII and
// capped in length, plus the canonical extension for the media type.
func sanitizeAttachmentName(img ImageAttachment) string {
	base := filepath.Base(img.Name)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	name := strings.Trim(b.String(), "._")
	// The builder only emits ASCII, so a byte slice here is rune-safe.
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		name = "image"
	}
	return name + attachmentExtensionForMediaType(img.MediaType)
}

// attachmentExtensionForMediaType maps an attachment's MIME type to its
// canonical file extension, defaulting to PNG: the composer surfaces
// re-encode everything to PNG, and unknown types still get a name a reader
// can make sense of.
func attachmentExtensionForMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/tiff", "image/tif":
		return ".tif"
	default:
		return ".png"
	}
}
