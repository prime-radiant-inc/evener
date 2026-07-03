// Package worktree holds the pure, dependency-light decision cores for
// serf's native git-worktree management (name validation, project
// identifiers, metadata-sidecar filename encoding). Nothing in this package
// touches the filesystem, git, or any other I/O; the manage_worktree tool
// (agent/session_tools_worktree.go) owns all side effects and calls these
// helpers to make its decisions.
package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// nameRe is the character-and-first-byte filter from the native worktree
// tools design (spec §2 "name validation"): underscore is a legal git-ref
// character and appears in serf's own generated ids (dlg_…, job_…), which §9
// uses as worktree names — a regex without it would reject the feature's own
// names.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)

// maxNameBytes is the byte cap on a worktree/branch name (spec §2).
const maxNameBytes = 100

// ValidateName reports whether name is legal as both a git branch name and a
// worktree path component. It is an early, dependency-free filter: the
// create path also runs `git check-ref-format --branch <name>` as the source
// of truth for ref validity (spec §2), so ValidateName does not need to be
// exhaustive over every git ref-format rule — but within the character
// alphabet it accepts, its accept set is kept a strict subset of what git's
// --branch mode accepts (consecutive/empty path components, a component
// leading with "." or ending in ".lock", and a trailing "." are all rejected
// here too) so callers never have to explain a confusing git error for a
// name serf itself blessed. ValidateName never touches git, the filesystem,
// or any other I/O.
func ValidateName(name string) error {
	if len(name) == 0 {
		return errors.New("worktree name is empty")
	}
	if len(name) > maxNameBytes {
		return fmt.Errorf("worktree name %q is %d bytes, exceeds the %d-byte limit", name, len(name), maxNameBytes)
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("worktree name %q must match %s", name, nameRe.String())
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("worktree name %q must not contain %q", name, "..")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("worktree name %q must not start with %q", name, "-")
	}
	if strings.HasSuffix(name, "/") {
		return fmt.Errorf("worktree name %q must not end with %q", name, "/")
	}
	if strings.Contains(name, "@{") {
		return fmt.Errorf("worktree name %q must not contain %q", name, "@{")
	}
	if strings.HasSuffix(name, ".") {
		return fmt.Errorf("worktree name %q must not end with %q", name, ".")
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" {
			return fmt.Errorf("worktree name %q must not contain an empty path component", name)
		}
		if strings.HasPrefix(component, ".") {
			return fmt.Errorf("worktree name %q has a path component %q starting with %q", name, component, ".")
		}
		if strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("worktree name %q has a path component %q ending with %q", name, component, ".lock")
		}
	}
	return nil
}

// maxBasenameBytes caps the human-legible prefix of a projectid (spec §6).
const maxBasenameBytes = 48

// basenameDisallowed matches every byte outside projectid's safe-basename
// alphabet.
var basenameDisallowed = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// ProjectID derives serf's stable per-repo-root identifier from the
// canonical (symlink-resolved) absolute repo root: a human-legible,
// filesystem-safe basename prefix plus a fixed-length content hash, so
// identically-named repos checked out at different paths never collide
// (spec §6). The hash is computed over the caller-supplied string verbatim;
// ProjectID does not itself resolve symlinks or canonicalize the path.
func ProjectID(canonicalAbsRoot string) string {
	base := basenameDisallowed.ReplaceAllString(filepath.Base(canonicalAbsRoot), "")
	base = strings.TrimLeft(base, ".-")
	if len(base) > maxBasenameBytes {
		base = base[:maxBasenameBytes]
	}
	if base == "" {
		base = "repo"
	}
	sum := sha256.Sum256([]byte(canonicalAbsRoot))
	return base + "-" + hex.EncodeToString(sum[:])[:16]
}

// sidecarEscape is the sole escape byte used by EncodeSidecarName. It is
// intentionally outside ValidateName's alphabet, so encode/decode stays
// injective.
const sidecarEscape = '%'

// EncodeSidecarName maps a validated worktree name to its metadata-sidecar
// basename, without the ".json" extension the caller appends. Every "/" in
// the name becomes the literal sequence "%2F": the sidecar directory is a
// flat namespace, not mirrored nesting, because mirrored nesting lets a
// regex-legal, git-legal name pair (e.g. "a" and "a.json/b") collide — file
// .meta/a.json vs directory .meta/a.json/ — making a legal branch name
// spuriously uncreatable depending on creation order (spec §6). "%" is
// outside ValidateName's alphabet, so this mapping is injective.
func EncodeSidecarName(name string) string {
	return strings.ReplaceAll(name, "/", "%2F")
}

// DecodeSidecarName reverses EncodeSidecarName. It returns ok=false for any
// input that could not have come from EncodeSidecarName — in particular any
// "%" not part of a literal "%2F" triple — which flags sidecar files serf
// did not write (spec §6 "unmanaged_meta").
func DecodeSidecarName(encoded string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(encoded); {
		if encoded[i] == sidecarEscape {
			if i+3 <= len(encoded) && encoded[i+1] == '2' && encoded[i+2] == 'F' {
				b.WriteByte('/')
				i += 3
				continue
			}
			return "", false
		}
		b.WriteByte(encoded[i])
		i++
	}
	return b.String(), true
}
