package sandbox

import (
	_ "embed"
	"fmt"
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

	read := buildAllowRule("file-read*", readGrants(rp, sessionTmp, ps))
	write := buildAllowRule("file-write*", writeGrants(rp, sessionTmp, ps))
	network := networkSection(rp)

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

// accessGrant is one component of an (allow file-read*/file-write* ...) rule: a
// subpath grant on a root param, with optional require-not exclusions (protected
// git surfaces or denylisted subpaths), each also a param.
type accessGrant struct {
	rootKey      string
	excludedKeys []string
}

// readGrants builds the read allow's grants from the spawned layer. ReadAnywhere
// (read-only, workspace-write) grants "/" minus every masked path; worktree-only
// (restricted) grants each read root minus the masked paths that fall beneath
// it, plus the session tmp.
func readGrants(rp ResolvedPolicy, sessionTmp string, ps *paramSet) []accessGrant {
	if rp.Spawned.Read == ReadAnywhere {
		g := accessGrant{rootKey: ps.define("READABLE_ROOT_0", "/")}
		for i, m := range rp.MaskedPaths {
			g.excludedKeys = append(g.excludedKeys,
				ps.define(fmt.Sprintf("READABLE_ROOT_0_EXCLUDED_%d", i), m))
		}
		return []accessGrant{g}
	}

	roots := dedupeRoots(appendNonEmpty(slices.Clone(rp.Spawned.ReadRoots), sessionTmp))
	grants := make([]accessGrant, 0, len(roots))
	for i, root := range roots {
		g := accessGrant{rootKey: ps.define(fmt.Sprintf("READABLE_ROOT_%d", i), root)}
		ei := 0
		for _, m := range rp.MaskedPaths {
			if pathUnder(m, root) {
				g.excludedKeys = append(g.excludedKeys,
					ps.define(fmt.Sprintf("READABLE_ROOT_%d_EXCLUDED_%d", i, ei), m))
				ei++
			}
		}
		grants = append(grants, g)
	}
	return grants
}

// writeGrants builds the write allow's grants: each spawned write root plus the
// session tmp, with the git config/hook surfaces that fall beneath a root
// excluded as require-not guards. The worktree root's grant thus covers all of
// its .git (objects, refs, index, logs, HEAD, COMMIT_EDITMSG, …) EXCEPT the
// protected config/hook surfaces — the same shape the bwrap backend produces
// (whole-worktree writable, config/hooks re-protected read-only), so a commit
// works while a core.hooksPath redirect cannot be persisted. A linked
// worktree's external git-metadata roots are separate write roots; the main
// repo's config, being under no write root, is write-denied by default while
// remaining readable.
func writeGrants(rp ResolvedPolicy, sessionTmp string, ps *paramSet) []accessGrant {
	roots := dedupeRoots(appendNonEmpty(slices.Clone(rp.Spawned.WriteRoots), sessionTmp))
	grants := make([]accessGrant, 0, len(roots))
	for i, root := range roots {
		g := accessGrant{rootKey: ps.define(fmt.Sprintf("WRITABLE_ROOT_%d", i), root)}
		ei := 0
		for _, p := range rp.Git.ProtectedPaths {
			if pathUnder(p, root) {
				g.excludedKeys = append(g.excludedKeys,
					ps.define(fmt.Sprintf("WRITABLE_ROOT_%d_EXCLUDED_%d", i, ei), p))
				ei++
			}
		}
		grants = append(grants, g)
	}
	return grants
}

// buildAllowRule renders an (allow <action> ...) rule over grants, or "" when
// there are none. Each grant is a (subpath (param KEY)); a grant with exclusions
// is wrapped in a (require-all ...) that subtracts each excluded param as BOTH a
// (literal ...) and a (subpath ...) — the two-form guard from Codex that also
// denies first-time creation of the protected path itself, not just writes
// beneath it.
func buildAllowRule(action string, grants []accessGrant) string {
	if len(grants) == 0 {
		return ""
	}
	comps := make([]string, 0, len(grants))
	for _, g := range grants {
		if len(g.excludedKeys) == 0 {
			comps = append(comps, subpathParam(g.rootKey))
			continue
		}
		parts := make([]string, 0, 1+2*len(g.excludedKeys))
		parts = append(parts, subpathParam(g.rootKey))
		for _, ek := range g.excludedKeys {
			parts = append(parts,
				"(require-not (literal (param "+quoteParam(ek)+")))",
				"(require-not (subpath (param "+quoteParam(ek)+")))")
		}
		comps = append(comps, "(require-all "+strings.Join(parts, " ")+")")
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
