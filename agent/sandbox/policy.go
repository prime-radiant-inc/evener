// Package sandbox owns serf's backend-independent sandbox policy model: the
// mode enum and its flag/config parsing, the default secrets+pseudo-filesystem
// denylist, host-capability facts (probed behind an injectable interface),
// git-surface resolution, and the resolver that turns a (mode, network, host,
// cwd) request into an exact ResolvedPolicy — the grants, denials, network
// decision, cache strategy, and enforcing backend that every backend (bwrap,
// Landlock, Seatbelt) is later held to via the exported contract harness.
//
// M1 carries the policy but enforces nothing: no file tool or spawned command
// consults a ResolvedPolicy yet. Enforcement arrives in M2 (in-process file
// tools) and M3 (Linux kernel wrapper); the user-facing --sandbox flag stays
// gated off until M5. See docs/superpowers/specs/2026-07-08-sandboxing-design.md.
package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Mode is the sandbox enforcement mode, selected once at session start and
// immutable for the session's lifetime — no tool call can relax it. The zero
// value is ModeOff, so an absent or nil policy is exactly today's behavior.
type Mode int

const (
	// ModeOff applies no sandbox: exactly today's behavior (reads anywhere,
	// writes confined to the working root as they are now). The zero value.
	ModeOff Mode = iota
	// ModeReadOnly denies all file-tool writes (session tmp excepted) and
	// confines spawned-process writes to tmp; reads are anywhere minus the
	// denylist. A read-only session cannot commit.
	ModeReadOnly
	// ModeWorkspaceWrite grants writes to the worktree, session tmp, and
	// contained caches; reads are anywhere minus the denylist.
	ModeWorkspaceWrite
	// ModeRestricted confines file-tool reads and writes to the worktree (plus
	// tmp); spawned processes additionally read system roots. The tightest mode.
	ModeRestricted
)

// modeNames maps each Mode to the name the --sandbox flag accepts and a session
// persists. Kept in enum order so AllModes and the round-trip stay aligned.
var modeNames = [...]string{
	ModeOff:            "off",
	ModeReadOnly:       "read-only",
	ModeWorkspaceWrite: "workspace-write",
	ModeRestricted:     "restricted",
}

// AllModes returns every Mode in enum order. Used by the round-trip test and by
// callers that enumerate the mode space (e.g. flag help, the contract table).
func AllModes() []Mode {
	return []Mode{ModeOff, ModeReadOnly, ModeWorkspaceWrite, ModeRestricted}
}

// String returns the mode's wire name, or "Mode(<n>)" for an out-of-range value
// (which never occurs for a value produced by ParseMode).
func (m Mode) String() string {
	if int(m) < 0 || int(m) >= len(modeNames) {
		return fmt.Sprintf("Mode(%d)", int(m))
	}
	return modeNames[m]
}

// ParseMode maps a mode name to its Mode, tolerating surrounding whitespace and
// case. An unknown name is a typed error rather than a silent default so a
// mistyped --sandbox value fails loudly instead of quietly disabling the box.
func ParseMode(name string) (Mode, error) {
	norm := strings.ToLower(strings.TrimSpace(name))
	for m, n := range modeNames {
		if n == norm {
			return Mode(m), nil
		}
	}
	return ModeOff, fmt.Errorf("unknown sandbox mode %q (want one of: off, read-only, workspace-write, restricted)", name)
}

// defaultPseudoFSPaths are the host pseudo-filesystems masked in every sandboxed
// mode, in BOTH the spawned-process and in-process file-tool layers. The
// file-tool masks matter independently: file-tool reads are "anywhere minus
// denylist", so without these a read_file("/proc/<serf-pid>/environ") would read
// serf's own environment — including the provider API key. "/run/user" masks the
// per-user runtime dir (agent sockets) and its subtree.
var defaultPseudoFSPaths = []string{
	"/proc",
	"/sys",
	"/dev/fd",
	"/dev/mem",
	"/run/user",
	// Privileged daemon control sockets. A read-only bind of / (read-only and
	// workspace-write modes) still exposes these, and a read-only bind mount does
	// NOT block connect() to a unix socket, nor does --unshare-net affect AF_UNIX.
	// So a session on a host where the invoking user can reach the docker/podman/
	// containerd/dbus socket could drive the daemon (e.g. `docker run -v /:/host`)
	// straight to host root, even with net=off. Masking the socket paths turns
	// connect() into ECONNREFUSED. (A broader /run policy is deferred: masking all
	// of /run would also hide legitimate runtime state; these are the well-known
	// escalation vectors.)
	"/run/docker.sock",
	"/var/run/docker.sock",
	"/run/podman/podman.sock",
	"/run/containerd/containerd.sock",
	"/run/dbus/system_bus_socket",
}

// defaultSecretHomePaths are the credential directories/files masked in every
// sandboxed mode, expressed relative to $HOME and resolved against the concrete
// home at policy-resolution time.
var defaultSecretHomePaths = []string{
	".ssh",
	".aws",
	".config/gcloud",
	".netrc",
	".config/serf",
	".gnupg",
	".docker/config.json",
	".kube",
	".git-credentials",
}

// DefaultDenylist returns the spec's default masked set as absolute paths: the
// host pseudo-filesystems plus every credential path resolved against home. The
// result is a freshly allocated slice on every call, so a caller mutating it
// cannot poison the shared defaults.
//
// Precondition: home must be an absolute path. The credential entries are
// home-relative, so a home of "" would yield relative paths the enforcement
// layers (which compare absolute paths) could never match — a silent unmask.
// Resolve guarantees an absolute home before calling this.
func DefaultDenylist(home string) []string {
	out := make([]string, 0, len(defaultPseudoFSPaths)+len(defaultSecretHomePaths))
	out = append(out, defaultPseudoFSPaths...)
	for _, rel := range defaultSecretHomePaths {
		out = append(out, filepath.Join(home, rel))
	}
	return out
}

// SandboxPolicy is the immutable, backend-independent policy request assembled
// at session start from the flag/config layer: the mode, the network decision,
// user extensions to the denylist (add and remove), and extra roots. It is a
// plain value — there is no mutator on it, so nothing the model does mid-session
// can change it. Resolve turns it (plus host facts and cwd) into a ResolvedPolicy.
type SandboxPolicy struct {
	// Mode is the enforcement mode. The zero value (ModeOff) is today's behavior.
	Mode Mode

	// Network reports whether egress is allowed (--sandbox-net on). It is a
	// tri-state: nil means the unset default (ON when sandboxed), a non-nil value
	// is an explicit choice. A plain bool zero value would silently mean OFF, so a
	// SandboxPolicy{Mode: ModeRestricted} would disable network by accident;
	// Resolve collapses nil to on. Only meaningful for a non-off Mode. Mirrors
	// SessionConfig.SandboxNet, which uses the same nil-means-default convention.
	Network *bool

	// DenylistAdd extends the default masked set. Entries may be absolute
	// ("/opt/secret"), home-relative ("~/.foo"), or bare-relative (".foo",
	// joined to home). Human-configured only; never model-changeable.
	DenylistAdd []string

	// DenylistRemove punches holes in the default masked set (same path forms as
	// DenylistAdd). Human-configured only.
	DenylistRemove []string

	// ExtraWritableRoots and ExtraReadRoots are human-configured additional roots
	// folded into the resolved grants. Absolute paths.
	ExtraWritableRoots []string
	ExtraReadRoots     []string
}

// resolveHomePath turns a denylist/root entry into an absolute path: an absolute
// entry is cleaned as-is; a "~/x" or bare-relative "x" entry is joined to home.
func resolveHomePath(entry, home string) string {
	e := strings.TrimSpace(entry)
	if e == "" {
		return ""
	}
	if filepath.IsAbs(e) {
		return filepath.Clean(e)
	}
	if rest, ok := strings.CutPrefix(e, "~/"); ok {
		e = rest
	}
	return filepath.Clean(filepath.Join(home, e))
}

// EffectiveDenylist returns the absolute masked set for this policy resolved
// against home: the pseudo-filesystem floor (always present), plus the credential
// set extended by DenylistAdd and reduced by DenylistRemove. It is a pure function
// of the policy value and returns a fresh slice; it never mutates the policy or
// the shared defaults.
//
// The pseudo-fs floor (/proc, /sys, …) is NON-REMOVABLE: DenylistRemove applies
// only to the credential/user-added set. Masking /proc is load-bearing — it stops
// a read of /proc/<serf-pid>/environ from leaking serf's own provider API key —
// so a stray or malicious DenylistRemove of /proc can never re-open that path.
// Only the credential dirs (and user additions) are user-removable.
func (p SandboxPolicy) EffectiveDenylist(home string) []string {
	// Non-removable floor first.
	out := make([]string, 0, len(defaultPseudoFSPaths)+len(defaultSecretHomePaths)+len(p.DenylistAdd))
	out = append(out, defaultPseudoFSPaths...)

	// Removable set: default credentials + user additions.
	var removable []string
	for _, rel := range defaultSecretHomePaths {
		removable = appendUnique(removable, filepath.Join(home, rel))
	}
	for _, add := range p.DenylistAdd {
		if abs := resolveHomePath(add, home); abs != "" {
			removable = appendUnique(removable, abs)
		}
	}

	remove := make(map[string]struct{}, len(p.DenylistRemove))
	for _, r := range p.DenylistRemove {
		if abs := resolveHomePath(r, home); abs != "" {
			remove[abs] = struct{}{}
		}
	}
	for _, entry := range removable {
		if _, drop := remove[entry]; !drop {
			out = append(out, entry)
		}
	}
	return out
}
