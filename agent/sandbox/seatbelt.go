package sandbox

import (
	_ "embed"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// The static SBPL sections, embedded verbatim (mirrors Codex's include_str!): a
// deny-default base every policy starts from, the network service allows added
// only when egress is on, and the macOS system read-roots added only for
// restricted mode. Embedding (rather than generating) keeps them auditable as
// plain policy text and guarantees the base is byte-identical across every
// generated policy.
//
//go:embed seatbelt_base.sbpl
var seatbeltBasePolicy string

//go:embed seatbelt_network.sbpl
var seatbeltNetworkPolicy string

//go:embed seatbelt_platform_defaults.sbpl
var seatbeltPlatformDefaults string

// DirParam is a single `sandbox-exec -D KEY=path` definition. Every filesystem
// root, excluded subpath, and protected git surface reaches the SBPL policy ONLY
// as one of these params, referenced inside the policy text via (param "KEY").
// Path TEXT never enters the policy string, so a crafted worktree path
// containing a quote, paren, or newline cannot terminate a literal or open a new
// SBPL form — it is an opaque -D value the kernel substitutes. This is a
// stronger injection defense than escaping interpolated path text (there is no
// interpolation to escape), which is why the generator carries no SBPL string
// escaper: the only inputs that could be adversarial (paths) are fully
// param-ized. See FuzzSeatbeltPolicyNoInterpolation, which asserts the
// no-interpolation invariant against arbitrary path bytes.
type DirParam struct {
	Key  string
	Path string
}

// Canonicalizer resolves a filesystem root to the canonical (symlink-resolved)
// form Seatbelt matches on before it becomes a -D param value. macOS canonical
// paths differ from their conventional spellings (/tmp -> /private/tmp, /var ->
// /private/var, firmlinked $HOME under /Users), so a policy that referenced the
// conventional path would silently never match. It is injected so unit tests use
// the identity (host-independent golden output) and the darwin invocation path
// uses filepath.EvalSymlinks. A root that fails to canonicalize is passed
// through unchanged, never dropped (a dropped write root is a silent grant loss;
// a dropped exclusion is a silent widening).
type Canonicalizer func(string) string

// identityCanon returns paths unchanged — the unit-test canonicalizer, which
// keeps golden SBPL output independent of the host's real symlink layout.
func identityCanon(p string) string { return p }

// canonicalizeLongestPrefix resolves p to the canonical path Seatbelt matches on
// even when trailing components do not exist yet: it resolves the longest
// existing ancestor via eval (an EvalSymlinks-shaped resolver) and re-appends the
// non-existent tail. A not-yet-created protected surface (e.g.
// .git/config.worktree) therefore still inherits the canonical prefix
// (/tmp -> /private/tmp, /var -> /private/var) its existing parent has. This is
// load-bearing: Seatbelt matches a -D param string against the kernel's CANONICAL
// path, so an exclusion left in the conventional spelling while its granting root
// was canonicalized would silently miss and re-expose the protected surface. The
// darwin canonicalizer is realCanonicalizer (eval = filepath.EvalSymlinks); unit
// tests inject a fake so the walk is exercised on Linux.
func canonicalizeLongestPrefix(p string, eval func(string) (string, error)) string {
	p = filepath.Clean(p)
	if resolved, err := eval(p); err == nil {
		return resolved
	}
	dir := filepath.Dir(p)
	if dir == p {
		return p // reached the filesystem root with nothing resolvable
	}
	return filepath.Join(canonicalizeLongestPrefix(dir, eval), filepath.Base(p))
}

// SeatbeltPolicy turns a ResolvedPolicy's SPAWNED-process layer into an SBPL
// policy and its -D dir params. Seatbelt confines spawned commands (the shell,
// rg, MCP-stdio servers, hook commands) and every descendant they fork; the
// in-process file-tool layer (rp.FileTool) is enforced separately by M2, so this
// mirrors the bwrap backend in reading rp.Spawned.
//
// Sections are assembled deny-default base -> read allow -> write allow ->
// network allow -> platform defaults. Every section is additive on top of the
// base's (deny default): a granted path matches an (allow ...) rule, a denied
// path matches none and falls through to the default deny. net=off is therefore
// the ABSENCE of the network section, not an explicit deny — fail-closed by
// construction. The platform-defaults (macOS system read roots) are appended
// only for restricted mode, whose spawned layer reads the worktree only and so
// cannot dyld-load system frameworks without them; read-only and workspace-write
// read the whole disk minus the denylist and never include them.
//
// It is a pure function (no host or environment access beyond the injected
// canonicalizer) so its golden output and the M1 contract suite run on any host.
func SeatbeltPolicy(rp ResolvedPolicy, sessionTmp string, canon Canonicalizer) (string, []DirParam) {
	if canon == nil {
		canon = identityCanon
	}
	ps := &paramSet{canon: canon}

	read := buildAllowRule("file-read*", readRootKeys(rp, sessionTmp, ps))
	write := buildAllowRule("file-write*", writeRootKeys(rp, sessionTmp, ps))
	network := networkSection(rp)
	denials := denySection(rp, ps)

	sections := []string{seatbeltBasePolicy}
	if read != "" {
		sections = append(sections, "; read grants\n"+read)
	}
	if write != "" {
		sections = append(sections, "; write grants\n"+write)
	}
	if network != "" {
		sections = append(sections, "; network\n"+network)
	}
	// The macOS system read-roots a worktree-only process needs to exec/dyld-load.
	if rp.Mode == ModeRestricted {
		sections = append(sections, seatbeltPlatformDefaults)
	}
	// Authoritative denials come LAST so they override every allow above —
	// including the static platform-defaults' broad system grants. Under Seatbelt
	// the last matching rule wins, so an explicit (deny ...) is the only way to
	// carve a masked/protected path out of a broad allow; a require-not inside one
	// allow would leave a later allow (e.g. platform-defaults' /tmp) free to
	// re-grant it. (This mirrors Codex's own trailing (deny file-read* ...) for
	// unreadable globs.)
	if denials != "" {
		sections = append(sections, "; authoritative denials (override every allow above)\n"+denials)
	}

	return strings.Join(sections, "\n"), ps.params
}

// paramSet allocates uniquely-keyed -D dir params, applying the canonicalizer to
// each path exactly once as it is defined so callers never handle raw paths.
type paramSet struct {
	params []DirParam
	canon  Canonicalizer
}

// define records a KEY=canon(path) param and returns the key for reference in
// the policy text. Keys are generated (uppercase, digits, underscore) so they
// are always safe to quote directly — no path text ever becomes a key.
func (ps *paramSet) define(key, path string) string {
	ps.params = append(ps.params, DirParam{Key: key, Path: ps.canon(path)})
	return key
}

// readRootKeys allocates the read allow's root params. ReadAnywhere (read-only,
// workspace-write) grants "/" — reads are "anywhere minus the denylist", with the
// denylist subtracted authoritatively by denySection. Worktree-only (restricted)
// grants each read root plus the session tmp.
func readRootKeys(rp ResolvedPolicy, sessionTmp string, ps *paramSet) []string {
	if rp.Spawned.Read == ReadAnywhere {
		return []string{ps.define("READABLE_ROOT_0", "/")}
	}
	roots := dedupeRoots(appendNonEmpty(slices.Clone(rp.Spawned.ReadRoots), sessionTmp))
	keys := make([]string, 0, len(roots))
	for i, root := range roots {
		keys = append(keys, ps.defineRead(fmt.Sprintf("READABLE_ROOT_%d", i), root)...)
	}
	return keys
}

// defineRead records the read-grant param(s) for ONE read root: the canonical
// path, plus — when the root is named through a SYMLINK, so its literal spelling
// differs from that canonical path — a second KEY_LINK param for the literal
// spelling.
//
// Both names are needed, and neither alone widens the grant. Seatbelt evaluates
// the path a process ACTUALLY NAMES rather than the resolved one, so opening
// ~/.gitconfig with only its symlink TARGET granted is refused at the link
// itself ("unable to access '<home>/.gitconfig': Operation not permitted") —
// the exact shape in which a dotfile-managed global git config was unreadable
// under restricted mode. Verified against the live kernel in both directions:
// the converse grant, the link spelling alone, does NOT make an ungranted (or
// masked) target readable through the link. The literal param is therefore a
// SPELLING alias, never extra reach — the read-side counterpart of the firmlink
// alias defineDeny emits, for the same reason.
func (ps *paramSet) defineRead(key, root string) []string {
	keys := []string{ps.define(key, root)}
	if literal := filepath.Clean(root); ps.canon(root) != literal {
		linkKey := key + "_LINK"
		ps.params = append(ps.params, DirParam{Key: linkKey, Path: literal})
		keys = append(keys, linkKey)
	}
	return keys
}

// writeRootKeys allocates the write allow's root params: each spawned write root
// plus the session tmp. The worktree root grants all of its .git (objects, refs,
// index, logs, HEAD, COMMIT_EDITMSG, …) — matching the bwrap backend, which binds
// the whole worktree writable — and denySection then subtracts the protected
// config/hook surfaces, so a commit works while a core.hooksPath redirect cannot
// be persisted. A linked worktree's external git-metadata roots are separate
// write roots; the main repo's config is under no write root (write-denied) yet
// stays readable.
func writeRootKeys(rp ResolvedPolicy, sessionTmp string, ps *paramSet) []string {
	roots := dedupeRoots(appendNonEmpty(slices.Clone(rp.Spawned.WriteRoots), sessionTmp))
	keys := make([]string, 0, len(roots))
	for i, root := range roots {
		keys = append(keys, ps.define(fmt.Sprintf("WRITABLE_ROOT_%d", i), root))
	}
	return keys
}

// denySection emits the authoritative trailing denials, each path a -D param
// referenced as BOTH a (literal ...) (the path itself, denying first-time
// creation) and a (subpath ...) (everything beneath it):
//
//   - Every masked path (secrets + pseudo-fs denylist) is denied for read AND
//     write, so no allow — the "/" read grant, a granted read root, or the
//     platform-defaults' broad system reads — can re-expose it. /dev/fd is the one
//     exception: it is process-local on macOS (the child's own fd table; serf's
//     fds never leak in) and is needed for shell process substitution, so the base
//     grants it and it is NOT re-denied here — the same treatment bwrap gives it
//     via its minimal --dev. The resolver still lists /dev/fd in MaskedPaths; the
//     backend realizes it safely rather than by denial.
//   - Every protected git surface (config, config.worktree, hooks, the .git
//     pointer, submodule configs, resolved config includes) is denied for WRITE
//     only (reads are allowed — git must read config), so a write to it is denied
//     even when it falls under a broad allow (e.g. a worktree placed under /tmp,
//     which platform-defaults would otherwise make writable).
//
// Each deny is emitted for BOTH firmlink spellings of its path (see defineDeny):
// macOS firmlinks give a data-volume file a second real path that EvalSymlinks
// does not collapse, so a deny for one spelling alone is bypassable via the other.
func denySection(rp ResolvedPolicy, ps *paramSet) string {
	var rules []string
	mi := 0
	for _, m := range rp.MaskedPaths {
		if isDeviceFloorException(m) {
			continue
		}
		for _, k := range ps.defineDeny(fmt.Sprintf("MASKED_%d", mi), m) {
			rules = append(rules, "(deny file-read* file-write* "+literalAndSubpath(k)+")")
		}
		mi++
	}
	pi := 0
	for _, p := range rp.Git.ProtectedPaths {
		for _, k := range ps.defineDeny(fmt.Sprintf("PROTECTED_%d", pi), p) {
			rules = append(rules, "(deny file-write* "+literalAndSubpath(k)+")")
		}
		pi++
	}
	return strings.Join(rules, "\n")
}

// dataVolumePrefix is the APFS data-volume mount point macOS firmlinks a file's
// root-level spelling onto: /Users/x and /System/Volumes/Data/Users/x are the
// same inode. filepath.EvalSymlinks does not collapse a firmlink, so both
// spellings reach the kernel unresolved and each must be denied independently.
const dataVolumePrefix = "/System/Volumes/Data"

// firmlinkAlias returns the OTHER firmlink spelling of an already-canonical macOS
// path: the stripped root spelling for a path already under the data volume, or
// the data-volume alias for any other path. It returns path unchanged only for
// the data-volume mount point itself, which has no distinct alias.
func firmlinkAlias(path string) string {
	if path == dataVolumePrefix {
		return path
	}
	if rest, ok := strings.CutPrefix(path, dataVolumePrefix+"/"); ok {
		return "/" + rest
	}
	return dataVolumePrefix + path
}

// defineDeny records a deny-param KEY=canon(path) plus, when the canonical path
// has a distinct firmlink spelling, a second KEY_ALIAS param for that spelling,
// and returns the key(s) to reference in the deny rule. Emitting a deny for both
// spellings closes the firmlink-alias bypass (see firmlinkAlias). It is used only
// by the Seatbelt policy generator, so the alias transform — which is macOS
// specific — never reaches the bwrap backend, which builds its own denials.
func (ps *paramSet) defineDeny(key, path string) []string {
	canonical := ps.canon(path)
	ps.params = append(ps.params, DirParam{Key: key, Path: canonical})
	keys := []string{key}
	if alias := firmlinkAlias(canonical); alias != canonical {
		aliasKey := key + "_ALIAS"
		ps.params = append(ps.params, DirParam{Key: aliasKey, Path: alias})
		keys = append(keys, aliasKey)
	}
	return keys
}

// isDeviceFloorException reports whether a masked path is one the base policy
// intentionally grants and must not be re-denied: /dev/fd (process-local, needed
// for stdio/process-substitution).
func isDeviceFloorException(path string) bool {
	return path == "/dev/fd"
}

// literalAndSubpath renders the two filters that together deny a path itself and
// everything beneath it: (literal (param K)) (subpath (param K)).
func literalAndSubpath(key string) string {
	return "(literal (param " + quoteParam(key) + ")) (subpath (param " + quoteParam(key) + "))"
}

// buildAllowRule renders an (allow <action> (subpath (param KEY)) ...) rule over
// the given root param keys, or "" when there are none. Masked/protected paths
// that fall under a root are NOT carved out here; denySection subtracts them
// authoritatively after every allow.
func buildAllowRule(action string, rootKeys []string) string {
	if len(rootKeys) == 0 {
		return ""
	}
	comps := make([]string, 0, len(rootKeys))
	for _, k := range rootKeys {
		comps = append(comps, subpathParam(k))
	}
	return "(allow " + action + "\n  " + strings.Join(comps, "\n  ") + "\n)"
}

// networkSection is the network allow, appended only when egress is on. The
// blanket outbound/inbound allows open the socket layer; the embedded network
// policy adds the platform service lookups (DNS, SecurityServer, configd) a
// process needs to resolve names and validate TLS. net=off returns "" — the
// base's (deny default) then blocks all egress with no explicit deny.
func networkSection(rp ResolvedPolicy) string {
	if !rp.Network {
		return ""
	}
	return "(allow network-outbound)\n(allow network-inbound)\n" + seatbeltNetworkPolicy
}

// seatbeltArgs assembles the full sandbox-exec argv:
//
//	[execPath, "-p", policyText, "-DKEY=path"..., "--", argv...]
//
// It is pure (execPath is passed in, not resolved) so it is exercised on any
// host; the darwin wrapper supplies the hard-coded /usr/bin/sandbox-exec path.
// The "--" separates sandbox-exec's own flags from the confined command, and
// each -D param carries a root/exclusion path out-of-band so no path text enters
// the policy string.
func seatbeltArgs(execPath, policyText string, params []DirParam, argv []string) []string {
	out := make([]string, 0, 3+len(params)+1+len(argv))
	out = append(out, execPath, "-p", policyText)
	for _, p := range params {
		out = append(out, "-D"+p.Key+"="+p.Path)
	}
	out = append(out, "--")
	out = append(out, argv...)
	return out
}

// subpathParam renders a (subpath (param "KEY")) grant.
func subpathParam(key string) string { return "(subpath (param " + quoteParam(key) + "))" }

// quoteParam double-quotes a generated param key. Keys are produced by this
// package from a fixed alphabet (uppercase letters, digits, underscore), never
// from path text, so a raw double-quote wrap needs no escaping — TestParamKeys
// pins that invariant.
func quoteParam(key string) string { return `"` + key + `"` }

// appendNonEmpty appends v to s only when it is not blank, so a missing session
// tmp never becomes an empty grant root.
func appendNonEmpty(s []string, v string) []string {
	if strings.TrimSpace(v) != "" {
		return append(s, v)
	}
	return s
}
