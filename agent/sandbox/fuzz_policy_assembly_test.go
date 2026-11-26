//go:build evenerfuzz

package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

// FuzzSandboxPolicyAssembly exercises the pure policy and backend assembly
// boundary using only files created beneath t.TempDir. It deliberately builds
// bwrap and Seatbelt argv/policy text without starting either executable.
func FuzzSandboxPolicyAssembly(f *testing.F) {
	for _, seed := range []struct {
		program byte
		raw     string
	}{
		{0, ""},
		{1, "provider-name"},
		{2, `quote")newline\n`},
		{255, "  Read-Only  "},
	} {
		f.Add(seed.program, seed.raw)
	}
	f.Fuzz(func(t *testing.T, program byte, raw string) {
		if len(raw) > 4096 {
			return
		}
		fixture := newStructuralFixture(t, 0)
		home := filepath.Join(fixture.root, "home")
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatalf("mkdir home: %v", err)
		}

		fuzzPolicyModes(t, fixture, home, raw, program)
		fuzzEnvironmentFloor(t, fixture, home, raw, program)
		fuzzDeniedAndProviderHelpers(t, fixture, raw, program)
		fuzzSessionScratchAndProbeHelpers(t, fixture)
		fuzzGitMetadataHelpers(t, fixture)
		fuzzReRootAndWrapperBoundaries(t, fixture, home)
		fuzzBackendAssembly(t, fixture, program)
		fuzzSandboxResidualBranches(t)
	})
}

func fuzzPolicyModes(t *testing.T, fixture structuralFixture, home, raw string, program byte) {
	t.Helper()
	for _, mode := range append(AllModes(), Mode(-1), Mode(99)) {
		_ = mode.String()
		for _, parent := range AllModes() {
			_ = mode.AtLeastAsConfining(parent)
		}
	}
	for _, name := range []string{"", "off", " read-only ", "WORKSPACE-WRITE", "restricted", raw} {
		_, _ = ParseMode(name)
		_ = ModeIsOff(name)
	}
	for _, backend := range []Backend{BackendNone, BackendBwrap, BackendSeatbelt, Backend(99)} {
		_ = backend.String()
	}
	for _, strategy := range []CacheStrategy{CacheNone, CacheOverlay, CacheSessionPrivate, CacheStrategy(99)} {
		_ = strategy.String()
	}
	for _, scope := range []ReadScope{ReadAnywhere, ReadWorktreeOnly, ReadScope(99)} {
		_ = scope.String()
	}
	for _, kind := range []WorkspaceKind{NonGit, MainCheckout, LinkedWorktree, Submodule, WorkspaceKind(99)} {
		_ = kind.String()
	}

	policy := SandboxPolicy{
		Mode:               ModeWorkspaceWrite,
		DenylistAdd:        []string{"~/.added", raw, filepath.Join(home, "absolute-added")},
		DenylistRemove:     []string{"~/.aws", "/proc"},
		ExtraReadRoots:     []string{fixture.main, "   "},
		ExtraWritableRoots: []string{fixture.linked},
	}
	effective := policy.EffectiveDenylist(home)
	if !slices.Contains(effective, "/proc") {
		t.Fatal("EffectiveDenylist allowed removal of the non-removable /proc floor")
	}
	if slices.Contains(effective, filepath.Join(home, ".aws")) {
		t.Fatal("EffectiveDenylist failed to remove a user-removable credential path")
	}
	defaults := DefaultDenylist(home)
	if len(defaults) == 0 || !slices.Contains(defaults, filepath.Join(home, ".ssh")) {
		t.Fatalf("DefaultDenylist(%q) lost the credential floor: %v", home, defaults)
	}
	defaults[0] = "mutated"
	if DefaultDenylist(home)[0] == "mutated" {
		t.Fatal("DefaultDenylist returned shared mutable storage")
	}

	for _, mode := range AllModes() {
		net := program&1 == 0
		rp, err := Resolve(SandboxPolicy{
			Mode:               mode,
			Network:            &net,
			DenylistAdd:        policy.DenylistAdd,
			DenylistRemove:     policy.DenylistRemove,
			ExtraReadRoots:     policy.ExtraReadRoots,
			ExtraWritableRoots: policy.ExtraWritableRoots,
		}, HostFacts{
			OS: "linux", Home: home, BwrapPath: "/fixture/bwrap", BwrapCapable: true,
			OverlaySupported: program&2 == 0,
		}, fixture.main)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", mode, err)
		}
		if mode == ModeOff {
			if rp.Enforced() {
				t.Fatal("off policy resolved as enforced")
			}
			continue
		}
		if !rp.Enforced() || rp.Backend != BackendBwrap {
			t.Fatalf("enforced policy did not select fake bwrap: %+v", rp)
		}
		for _, root := range slices.Concat(rp.FileTool.ReadRoots, rp.FileTool.WriteRoots, rp.Spawned.ReadRoots, rp.Spawned.WriteRoots) {
			if rp.Masks(root) {
				t.Fatalf("mode %s granted masked %q through %q", mode, rp.MaskedPaths, root)
			}
		}
		if got := EnforcementLine(rp); got == "" {
			t.Fatalf("enforced policy %s has no enforcement line", mode)
		}
	}

	// ResolveNamed must bypass host resolution for off, reject invalid persisted
	// values, and resolve a valid persisted mode through the same pure resolver.
	if rp, err := ResolveNamed(" off ", nil, HostFacts{}, fixture.main, nil); err != nil || rp != nil {
		t.Fatalf("ResolveNamed(off) = (%v, %v), want (nil, nil)", rp, err)
	}
	if _, err := ResolveNamed("not-a-mode", nil, HostFacts{}, fixture.main, nil); err == nil {
		t.Fatal("ResolveNamed accepted an invalid mode")
	}
	net := true
	if rp, err := ResolveNamed("restricted", &net, HostFacts{OS: "darwin", Home: home, SandboxExecPath: "/fixture/sandbox-exec"}, fixture.main, nil); err != nil || rp == nil || rp.Backend != BackendSeatbelt {
		t.Fatalf("ResolveNamed restricted seatbelt = (%+v, %v)", rp, err)
	}

	// A relative extra root is a fail-closed startup refusal, not a malformed
	// grant that later fails to match enforcement paths.
	if _, err := Resolve(SandboxPolicy{Mode: ModeRestricted, ExtraReadRoots: []string{"relative"}}, HostFacts{
		OS: "linux", Home: home, BwrapPath: "/fixture/bwrap", BwrapCapable: true,
	}, fixture.main); err == nil {
		t.Fatal("Resolve accepted a relative extra root")
	}
}

func fuzzEnvironmentFloor(t *testing.T, fixture structuralFixture, home, raw string, program byte) {
	t.Helper()
	sessionTmp := filepath.Join(fixture.root, "session-tmp")
	insideKube := filepath.Join(fixture.main, "kubeconfig")
	externalKube := filepath.Join(fixture.root, "external-kubeconfig")
	policy := ResolvedPolicy{
		Mode:          ModeRestricted,
		CacheStrategy: []CacheStrategy{CacheNone, CacheOverlay, CacheSessionPrivate}[int(program)%3],
		Git:           GitLayout{WorktreeRoot: fixture.main},
		FileTool:      AccessScope{Read: ReadWorktreeOnly, ReadRoots: []string{fixture.main}, WriteRoots: []string{fixture.main}},
		Spawned:       AccessScope{Read: ReadWorktreeOnly, ReadRoots: []string{fixture.main, home}, WriteRoots: []string{fixture.main}},
	}
	input := []string{
		"KEEP=" + raw,
		"not-an-assignment",
		"SSH_AUTH_SOCK=/fixture/agent.sock",
		"AWS_ACCESS_KEY_ID=secret",
		"KUBECONFIG=" + insideKube + string(os.PathListSeparator) + externalKube,
		"TMPDIR=/ambient/tmp",
		"GOCACHE=/ambient/go",
		"npm_config_cache=/ambient/npm",
		"CARGO_HOME=/ambient/cargo",
	}
	got := ApplyEnvFloor(input, policy, sessionTmp)
	if slices.Contains(got, "SSH_AUTH_SOCK=/fixture/agent.sock") || slices.Contains(got, "AWS_ACCESS_KEY_ID=secret") {
		t.Fatalf("env floor retained secret-bearing entries: %v", got)
	}
	if fuzzEnvHasName(got, "KUBECONFIG") {
		t.Fatalf("env floor retained external KUBECONFIG: %v", got)
	}
	if kubeconfigIsExternal("relative"+string(os.PathListSeparator)+insideKube, policy) {
		t.Fatalf("kubeconfigIsExternal rejected only in-scope/relative entries")
	}
	if !slices.Contains(got, "TMPDIR="+sessionTmp) {
		t.Fatalf("env floor did not redirect TMPDIR: %v", got)
	}
	if policy.CacheStrategy == CacheSessionPrivate {
		for _, name := range []string{"GOCACHE", "npm_config_cache", "CARGO_HOME"} {
			if value, ok := fuzzEnvValue(got, name); !ok || !identifier.PathUnder(value, sessionTmp) {
				t.Fatalf("env floor did not redirect %s into %q: %v", name, sessionTmp, got)
			}
		}
	}
	if len(got) > 0 {
		original := input[0]
		got[0] = "MUTATED=1"
		if input[0] != original {
			t.Fatal("ApplyEnvFloor mutated its input slice")
		}
	}

	scrubbed := ScrubSecretEnv([]string{"OPENAI_API_KEY=secret", "SAFE=" + raw, "not-an-assignment"})
	if fuzzEnvHasName(scrubbed, "OPENAI_API_KEY") || !slices.Contains(scrubbed, "SAFE="+raw) {
		t.Fatalf("ScrubSecretEnv result = %v", scrubbed)
	}
	for _, name := range []string{"api_key", "SECRET", "token", "password", "credential", "ordinary"} {
		_ = IsSecretEnvName(name)
	}
}

func fuzzDeniedAndProviderHelpers(t *testing.T, fixture structuralFixture, raw string, program byte) {
	t.Helper()
	d := &DeniedError{
		Mode:       []Mode{ModeOff, ModeReadOnly, ModeWorkspaceWrite, ModeRestricted}[int(program)%4],
		Tool:       "write_file",
		Path:       filepath.Join(fixture.root, "secret", "id_rsa"),
		Reason:     "fixture refusal",
		Sensitive:  program&1 != 0,
		ReasonKind: DenialReason(program % 12),
	}
	got, ok := AsDenied(fmt.Errorf("wrapped: %w", d))
	if !ok || got != d {
		t.Fatalf("AsDenied lost wrapped denial: (%+v, %v)", got, ok)
	}
	if _, ok := AsDenied(errors.New("ordinary")); ok {
		t.Fatal("AsDenied accepted a non-denial error")
	}
	if d.Sensitive && strings.Contains(d.Error(), filepath.Base(d.Path)) {
		t.Fatalf("sensitive error leaked basename: %q", d.Error())
	}
	_ = d.Redacted()
	if (&DeniedError{}).Redacted() != "" || (&DeniedError{Path: "relative/path"}).Redacted() != "relative/path" {
		t.Fatal("DeniedError.Redacted lost empty or relative path handling")
	}
	_ = NewEscalationRequest(raw, d)
	d.Command = "fixture-command"
	_ = NewEscalationRequest(raw, d)
	for reason := DenialUnspecified; reason <= DenialRootTarget+2; reason++ {
		_ = reason.Curable()
	}
	for _, provider := range []string{"openai", "anthropic", "gemini", "unknown-" + raw} {
		_, _ = WebEgress(provider)
		_ = ProviderWebAllowedUnderNetOff(provider)
	}
}

func fuzzSessionScratchAndProbeHelpers(t *testing.T, fixture structuralFixture) {
	t.Helper()
	tmp, err := NewSessionScratch(fixture.root, fixture.main)
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp.Dir, "state"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write session tmp: %v", err)
	}
	if err := tmp.Cleanup(); err != nil {
		t.Fatalf("SessionScratch.Cleanup: %v", err)
	}
	if _, err := os.Stat(tmp.Dir); !os.IsNotExist(err) {
		t.Fatalf("SessionScratch.Cleanup left %q behind: %v", tmp.Dir, err)
	}
	var nilTmp *SessionScratch
	if err := nilTmp.Cleanup(); err != nil {
		t.Fatalf("nil SessionScratch cleanup: %v", err)
	}
	sweepCrashedSessionScratch(filepath.Join(fixture.root, "missing-base"))

	sweepBase := filepath.Join(fixture.root, "session-sweep")
	stale := filepath.Join(sweepBase, sessionScratchPrefix+"stale")
	fresh := filepath.Join(sweepBase, sessionScratchPrefix+"fresh")
	ordinary := filepath.Join(sweepBase, "ordinary")
	for _, dir := range []string{stale, fresh, ordinary} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir session sweep fixture %q: %v", dir, err)
		}
	}
	if err := os.Chtimes(stale, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatalf("age stale session fixture: %v", err)
	}
	sweepCrashedSessionScratch(sweepBase)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale session dir survived sweep: %v", err)
	}
	for _, dir := range []string{fresh, ordinary} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("session sweep removed non-stale fixture %q: %v", dir, err)
		}
	}
	fileBase := filepath.Join(fixture.root, "not-a-session-tmp-base")
	if err := os.WriteFile(fileBase, []byte("x"), 0o600); err != nil {
		t.Fatalf("write non-directory session base: %v", err)
	}
	oldCache := sessionScratchUserCacheDir
	sessionScratchUserCacheDir = func() (string, error) { return fileBase, nil }
	if _, err := NewSessionScratch(fileBase, fixture.main); err == nil {
		t.Fatal("NewSessionScratch accepted a file as its only base directory")
	}
	sessionScratchUserCacheDir = oldCache

	args := bwrapProbeArgs("/fixture/bwrap")
	if len(args) == 0 || args[0] != "/fixture/bwrap" || !slices.Contains(args, "--new-session") {
		t.Fatalf("bwrap probe argv lost hardening flags: %v", args)
	}
	if got := string(trimTrailingNewline([]byte("kernel\r\n"))); got != "kernel" {
		t.Fatalf("trimTrailingNewline = %q", got)
	}
}

func fuzzGitMetadataHelpers(t *testing.T, fixture structuralFixture) {
	t.Helper()
	gitDir := filepath.Join(fixture.main, ".git")
	writable := []string{fixture.main, filepath.Join(gitDir, "objects")}
	if !gitDirUnderModules(filepath.Join(gitDir, "modules", "nested", "child")) {
		t.Fatal("gitDirUnderModules missed a nested module gitdir")
	}
	if gitDirUnderModules(filepath.Join(fixture.root, "modules", "not-git")) {
		t.Fatal("gitDirUnderModules accepted a non-git modules path")
	}
	if !globCouldReachWritable("../*.conf", gitDir, writable) {
		t.Fatal("globCouldReachWritable missed a writable literal base")
	}
	if globCouldReachWritable("/etc/*.conf", gitDir, writable) {
		t.Fatal("globCouldReachWritable marked a non-writable system base")
	}
	if !globCouldReachWritable("*.conf", fixture.main, []string{fixture.main}) {
		t.Fatal("globCouldReachWritable missed a config-dir-relative glob")
	}
	if _, err := ClassifyWorkspace(fixture.malformed, nil); err == nil {
		t.Fatal("ClassifyWorkspace accepted an invalid .git pointer")
	}
	broken := filepath.Join(fixture.root, "broken-pointer")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatalf("mkdir broken pointer fixture: %v", err)
	}
	if err := os.Symlink(filepath.Join(fixture.root, "missing-gitdir"), filepath.Join(broken, ".git")); err != nil {
		t.Fatalf("symlink broken pointer fixture: %v", err)
	}
	if _, err := ClassifyWorkspace(broken, nil); err == nil {
		t.Fatal("ClassifyWorkspace accepted an unreadable .git pointer")
	}
	unknown := filepath.Join(fixture.root, "unknown-pointer")
	if err := os.MkdirAll(unknown, 0o755); err != nil {
		t.Fatalf("mkdir unknown pointer fixture: %v", err)
	}
	writeStructuralFile(t, filepath.Join(unknown, ".git"), "gitdir: ../not-a-worktree\n")
	if _, err := ClassifyWorkspace(unknown, nil); err == nil {
		t.Fatal("ClassifyWorkspace accepted an unrecognized relative pointer shape")
	}

	paths := parseIncludePaths(strings.Join([]string{
		"# ignored",
		"[include] path = one.conf",
		"[includeIf \"gitdir:fixture\"]",
		"path = \"two\\\".conf\"",
		"[other] path = ignored.conf",
		"[include] path = three.conf # comment",
		"[include",
		"path = four.conf",
	}, "\n"))
	if !slices.Contains(paths, "one.conf") || !slices.Contains(paths, "two\".conf") || !slices.Contains(paths, "three.conf") {
		t.Fatalf("parseIncludePaths = %v", paths)
	}
	_ = includePathValue("not-a-key = ignored")
	_ = includePathValue("path = \"unterminated")
	for _, tc := range []struct {
		value string
		want  string
		ok    bool
	}{
		{value: `"quote\".conf"`, want: `quote".conf`, ok: true},
		{value: `"slash\\.conf"`, want: `slash\.conf`, ok: true},
		{value: `"line\nname"`, want: "line\nname", ok: true},
		{value: `"tab\tname"`, want: "tab\tname", ok: true},
		{value: `"back\bname"`, want: "back\bname", ok: true},
		{value: `"bad\q"`, ok: false},
		{value: `"trailing\`, ok: false},
	} {
		got, ok := quotedIncludePathValue(tc.value)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("quotedIncludePathValue(%q) = (%q, %v), want (%q, %v)", tc.value, got, ok, tc.want, tc.ok)
		}
	}

	if found, err := scanConfigIncludes(filepath.Join(gitDir, "config"), writable, map[string]bool{}, 0); err != nil || len(found) == 0 {
		t.Fatalf("scanConfigIncludes safe chain = (%v, %v)", found, err)
	}
	if found, err := scanConfigIncludes(filepath.Join(fixture.root, "missing-config"), writable, map[string]bool{}, 0); err != nil || found != nil {
		t.Fatalf("scanConfigIncludes missing config = (%v, %v)", found, err)
	}
	if found, err := scanConfigIncludes(filepath.Join(gitDir, "config"), writable, map[string]bool{filepath.Join(gitDir, "config"): true}, 0); err != nil || found != nil {
		t.Fatalf("scanConfigIncludes seen config = (%v, %v)", found, err)
	}
	if _, err := scanConfigIncludes(filepath.Join(gitDir, "config"), writable, map[string]bool{}, gitConfigIncludeMaxDepth+1); err == nil {
		t.Fatal("scanConfigIncludes accepted an over-depth chain")
	}
	globConfig := filepath.Join(fixture.root, "writable-glob.conf")
	writableGlob := filepath.Join(fixture.main, "*.conf")
	if !globCouldReachWritable(writableGlob, filepath.Dir(globConfig), writable) {
		t.Fatalf("globCouldReachWritable(%q) lost a writable absolute base", writableGlob)
	}
	writeStructuralFile(t, globConfig, "[include]\npath = \""+writableGlob+"\"\n")
	if data, err := os.ReadFile(globConfig); err != nil {
		t.Fatalf("read writable glob config: %v", err)
	} else if parsed := parseIncludePaths(string(data)); !slices.Contains(parsed, writableGlob) {
		t.Fatalf("parseIncludePaths(%q) = %v", data, parsed)
	}
	if _, err := scanConfigIncludes(globConfig, writable, map[string]bool{}, 0); err == nil {
		t.Fatal("scanConfigIncludes accepted a writable glob include")
	}
	nonWritableGlob := filepath.Join(fixture.root, "system-glob.conf")
	writeStructuralFile(t, nonWritableGlob, "[include]\npath = /etc/*.conf\n[include]\npath = ~/.gitconfig\n")
	if found, err := scanConfigIncludes(nonWritableGlob, writable, map[string]bool{}, 0); err != nil || found != nil {
		t.Fatalf("scanConfigIncludes non-writable glob = (%v, %v)", found, err)
	}

	if got := removeProtectedFromWritable([]string{fixture.main, filepath.Join(fixture.main, "nested"), filepath.Join(fixture.root, "other")}, []string{fixture.main}); !slices.Equal(got, []string{filepath.Join(fixture.root, "other")}) {
		t.Fatalf("removeProtectedFromWritable = %v", got)
	}
	if !isUnderAnyRoot(filepath.Join(fixture.main, "nested"), []string{fixture.main}) || isUnderAnyRoot(fixture.root, []string{fixture.main}) {
		t.Fatal("isUnderAnyRoot lost containment direction")
	}
	if _, _, ok := findGitEntry(filepath.Join(fixture.nonGit, "child"), nil); ok {
		t.Fatal("findGitEntry found metadata outside a repository")
	}
	if got := moduleConfigProtections(filepath.Join(fixture.root, "no-modules")); got != nil {
		t.Fatalf("moduleConfigProtections missing root = %v", got)
	}
	prunedCommon := filepath.Join(fixture.root, "pruned-common")
	for _, name := range []string{"objects", "refs", "logs", "worktrees"} {
		if err := os.MkdirAll(filepath.Join(prunedCommon, "modules", name, "nested"), 0o755); err != nil {
			t.Fatalf("mkdir pruned module fixture %q: %v", name, err)
		}
	}
	if got := moduleConfigProtections(prunedCommon); got != nil {
		t.Fatalf("moduleConfigProtections pruned-only tree = %v", got)
	}
}

func fuzzReRootAndWrapperBoundaries(t *testing.T, fixture structuralFixture, home string) {
	t.Helper()
	main, laneA, laneB := structuralReRootLanes(t)
	net := true
	facts := HostFacts{OS: "linux", Home: home, BwrapPath: "/fixture/bwrap", BwrapCapable: true, OverlaySupported: true}
	for _, mode := range []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeRestricted} {
		rp, err := Resolve(SandboxPolicy{Mode: mode, Network: &net}, facts, laneA)
		if err != nil {
			t.Fatalf("Resolve(%s) re-root fixture: %v", mode, err)
		}
		if rp.Inputs().Mode != mode || rp.HostBwrapPath() != facts.BwrapPath || rp.HostBinaryPath() != facts.BwrapPath {
			t.Fatalf("resolved re-root inputs lost mode/host: %+v", rp)
		}
		control, err := rp.ControlPolicy(main)
		if err != nil {
			t.Fatalf("ControlPolicy(%s): %v", mode, err)
		}
		registry := filepath.Join(main, ".git", "worktrees")
		if mode == ModeReadOnly {
			if len(control.FileTool.WriteRoots) != 0 || len(control.Spawned.WriteRoots) != 0 {
				t.Fatalf("read-only control policy widened writes: %+v", control)
			}
		} else if !isUnderAnyRoot(registry, control.Spawned.WriteRoots) || !isUnderAnyRoot(registry, control.FileTool.WriteRoots) {
			t.Fatalf("control policy lost worktree registry %q: %+v", registry, control)
		}
		rerooted, err := rp.ReRoot(laneB)
		if err != nil || rerooted == nil || rerooted.Git.WorktreeRoot != laneB {
			t.Fatalf("ReRoot(%s) = (%+v, %v)", mode, rerooted, err)
		}
		wrapper, err := NewWrapper(rp, rp.HostBinaryPath(), filepath.Join(fixture.root, "wrapper-tmp"))
		if err != nil {
			t.Fatalf("NewWrapper(%s): %v", mode, err)
		}
		if rerootedWrapper, err := wrapper.ReRoot(laneB); err != nil || rerootedWrapper == nil || rerootedWrapper.Policy().Git.WorktreeRoot != laneB {
			t.Fatalf("Wrapper.ReRoot(%s) = (%+v, %v)", mode, rerootedWrapper, err)
		}
		if _, err := wrapper.WithPolicy(ResolvedPolicy{Backend: BackendNone}); err == nil {
			t.Fatal("Wrapper.WithPolicy accepted a non-enforcing backend")
		}
		_ = wrapper.Wrap(nil, "")
	}

	seatbelt, err := Resolve(SandboxPolicy{Mode: ModeRestricted, Network: &net}, HostFacts{
		OS: "darwin", Home: home, SandboxExecPath: "/fixture/sandbox-exec",
	}, fixture.main)
	if err != nil || seatbelt.HostBinaryPath() != "/fixture/sandbox-exec" {
		t.Fatalf("seatbelt HostBinaryPath = (%q, %v)", seatbelt.HostBinaryPath(), err)
	}
	off, err := Resolve(SandboxPolicy{Mode: ModeOff}, facts, laneA)
	if err != nil {
		t.Fatalf("Resolve(off): %v", err)
	}
	if control, err := off.ControlPolicy(main); err != nil || control == nil || control.Enforced() || off.HostBinaryPath() != "" {
		t.Fatalf("off ControlPolicy = (%+v, %v)", control, err)
	}
	nonGit, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, facts, fixture.nonGit)
	if err != nil {
		t.Fatalf("Resolve(non-git control policy): %v", err)
	}
	if control, err := nonGit.ControlPolicy(fixture.nonGit); err != nil || control == nil || control.Git.CommonDir != "" {
		t.Fatalf("non-git ControlPolicy = (%+v, %v)", control, err)
	}

	var nilPolicy *ResolvedPolicy
	if got, err := nilPolicy.ReRoot(laneB); err != nil || got != nil {
		t.Fatalf("nil ReRoot = (%+v, %v)", got, err)
	}
	if got, err := nilPolicy.ControlPolicy(main); err != nil || got != nil {
		t.Fatalf("nil ControlPolicy = (%+v, %v)", got, err)
	}
	handBuilt := &ResolvedPolicy{Mode: ModeRestricted}
	if _, err := handBuilt.ReRoot(laneB); err == nil {
		t.Fatal("hand-built enforced policy re-rooted without retained inputs")
	}
	if _, err := handBuilt.ControlPolicy(main); err == nil {
		t.Fatal("hand-built enforced control policy lost its fail-closed refusal")
	}
	var nilWrapper *Wrapper
	raw := []string{"/fixture/raw"}
	if got := nilWrapper.Wrap(raw, main); !slices.Equal(got, raw) {
		t.Fatalf("nil Wrapper.Wrap = %v", got)
	}
	cmd := exec.Command("/fixture/raw") //nolint:noctx // never started; exercises nil confinement only
	cmd.Dir = fixture.root
	nilWrapper.Confine(cmd, main)
	if cmd.Dir != fixture.root {
		t.Fatalf("nil Wrapper.Confine changed command dir to %q", cmd.Dir)
	}
	if got, err := nilWrapper.ReRoot(laneB); err != nil || got != nil {
		t.Fatalf("nil Wrapper.ReRoot = (%+v, %v)", got, err)
	}
	if got, err := nilWrapper.WithPolicy(ResolvedPolicy{}); err != nil || got != nil {
		t.Fatalf("nil Wrapper.WithPolicy = (%+v, %v)", got, err)
	}

	if runtime.GOOS != "darwin" {
		if wrapped, err := seatbeltWrap([]string{"/fixture/raw"}, ResolvedPolicy{Backend: BackendSeatbelt}, "", main); err == nil || wrapped != nil {
			t.Fatalf("non-darwin seatbeltWrap = (%v, %v)", wrapped, err)
		}
		stub, err := NewWrapper(ResolvedPolicy{Backend: BackendSeatbelt}, "/fixture/sandbox-exec", "")
		if err != nil {
			t.Fatalf("NewWrapper(seatbelt stub): %v", err)
		}
		stubCmd := exec.Command("/fixture/raw") //nolint:noctx // never started; exercises fail-closed stub only
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("seatbelt wrapper bypassed the non-darwin fail-closed panic")
				}
			}()
			stub.Confine(stubCmd, main)
		}()
		if stubCmd.Dir != main {
			t.Fatalf("seatbelt Confine failed to set cwd before refusal: %q", stubCmd.Dir)
		}
	}
	if got := (&RefusalError{Mode: ModeRestricted, Net: false, Reason: "fixture"}).Error(); !strings.Contains(got, "network=false") || !strings.Contains(got, "fixture") {
		t.Fatalf("RefusalError.Error = %q", got)
	}
}

func fuzzBackendAssembly(t *testing.T, fixture structuralFixture, program byte) {
	t.Helper()
	root := filepath.Join(fixture.root, "assembly")
	work := filepath.Join(root, "work")
	readRoot := filepath.Join(root, "read")
	cache := filepath.Join(root, "cache")
	sessionTmp := filepath.Join(root, "session")
	maskedDir := filepath.Join(root, "masked-dir")
	maskedFile := filepath.Join(root, "masked-file")
	protected := filepath.Join(work, ".git", "config")
	for _, dir := range []string{work, readRoot, cache, sessionTmp, maskedDir, filepath.Dir(protected)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir assembly fixture %q: %v", dir, err)
		}
	}
	if err := os.WriteFile(maskedFile, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write masked file: %v", err)
	}
	if err := os.WriteFile(protected, []byte("[core]"), 0o644); err != nil {
		t.Fatalf("write protected config: %v", err)
	}
	alias := filepath.Join(root, "masked-alias")
	if err := os.Symlink(maskedDir, alias); err != nil {
		t.Fatalf("symlink masked alias: %v", err)
	}

	base := ResolvedPolicy{
		Mode:          ModeRestricted,
		Network:       program&1 == 0,
		Backend:       BackendBwrap,
		CacheStrategy: CacheOverlay,
		CacheRoots:    []string{cache, filepath.Join(root, "missing-cache")},
		Spawned: AccessScope{
			Read:       ReadWorktreeOnly,
			ReadRoots:  []string{work, readRoot},
			WriteRoots: []string{work, filepath.Join(root, "missing-write")},
		},
		MaskedPaths: []string{maskedDir, maskedFile, alias, filepath.Join(root, "missing-mask"), "/proc", "/dev/fd"},
		Git: GitLayout{ProtectedPaths: []string{
			protected,
			filepath.Join(work, ".git", "hooks"),
			filepath.Join(work, ".git", "config.worktree"),
			filepath.Join(root, "outside-protected"),
		}},
	}
	for _, read := range []ReadScope{ReadAnywhere, ReadWorktreeOnly} {
		for _, network := range []bool{true, false} {
			rp := base
			rp.Network = network
			rp.Spawned.Read = read
			args := buildBwrapArgv(rp, sessionTmp, work)
			if slices.Contains(args, "--") || !slices.Contains(args, "--unshare-pid") || !slices.Contains(args, "--proc") {
				t.Fatalf("invalid bwrap assembly: %v", args)
			}
			if slices.Contains(args, "--unshare-net") != !network {
				t.Fatalf("network=%v bwrap args=%v", network, args)
			}
			if read == ReadAnywhere && !hasAssemblySeq(args, "--ro-bind", "/", "/") {
				t.Fatalf("read-anywhere policy lost root bind: %v", args)
			}
			if read == ReadWorktreeOnly && !hasAssemblySeq(args, "--tmpfs", "/") {
				t.Fatalf("restricted policy lost empty root: %v", args)
			}
		}
	}

	wrapper, err := NewWrapper(base, "/fixture/bwrap", sessionTmp)
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	if wrapper.Policy().Backend != BackendBwrap || wrapper.SessionTmp() != sessionTmp {
		t.Fatalf("wrapper retained wrong policy/session: %+v", wrapper)
	}
	wrapped := wrapper.Wrap([]string{"/fixture/tool", "--flag"}, work)
	if len(wrapped) == 0 || wrapped[0] != "/fixture/bwrap" || !slices.Contains(wrapped, "--") {
		t.Fatalf("wrapper argv = %v", wrapped)
	}
	cmd := exec.Command("/fixture/tool", "--flag") //nolint:noctx // never started; validates argv assembly only
	wrapper.Confine(cmd, work)
	if cmd.Path != "/fixture/bwrap" || cmd.Args[0] != "/fixture/bwrap" {
		t.Fatalf("Confine did not rewrite command: path=%q args=%v", cmd.Path, cmd.Args)
	}
	if next, err := wrapper.WithPolicy(base); err != nil || next == nil {
		t.Fatalf("Wrapper.WithPolicy = (%v, %v)", next, err)
	}
	if _, err := wrapper.ReRoot(work); err == nil {
		t.Fatal("hand-built wrapper policy unexpectedly re-rooted without retained inputs")
	}
	if _, err := NewWrapper(ResolvedPolicy{Backend: BackendNone}, "/fixture/bwrap", sessionTmp); err == nil {
		t.Fatal("NewWrapper accepted BackendNone")
	}
	if _, err := NewWrapper(base, "relative/bwrap", sessionTmp); err == nil {
		t.Fatal("NewWrapper accepted a relative binary path")
	}

	for _, mode := range []Mode{ModeWorkspaceWrite, ModeRestricted} {
		rp := base
		rp.Mode = mode
		text, params := SeatbeltPolicy(rp, sessionTmp, nil)
		if len(params) == 0 || strings.Contains(text, work) || !strings.Contains(text, "(deny default)") {
			t.Fatalf("seatbelt assembly leaked path or lost params: text=%q params=%v", text, params)
		}
		argv := seatbeltArgs("/fixture/sandbox-exec", text, params, []string{"/fixture/tool", "--flag"})
		if len(argv) < 5 || argv[0] != "/fixture/sandbox-exec" || !slices.Contains(argv, "--") {
			t.Fatalf("seatbelt argv = %v", argv)
		}
	}
	eval := func(path string) (string, error) {
		if path == "/virtual" {
			return "/canonical", nil
		}
		return "", errors.New("missing")
	}
	if got := canonicalizeLongestPrefix("/virtual/missing/leaf", eval); got != "/canonical/missing/leaf" {
		t.Fatalf("canonicalizeLongestPrefix = %q", got)
	}
	if got := canonicalizeLongestPrefix("/", func(string) (string, error) { return "", errors.New("missing") }); got != "/" {
		t.Fatalf("canonicalizeLongestPrefix root = %q", got)
	}
	ps := &paramSet{canon: identityCanon}
	if got := readRootKeys(ResolvedPolicy{Spawned: AccessScope{Read: ReadWorktreeOnly}}, "", ps); len(got) != 0 || buildAllowRule("file-read*", nil) != "" {
		t.Fatalf("empty Seatbelt read roots = %v", got)
	}
	if got := appendNonEmpty([]string{"keep"}, "  "); !slices.Equal(got, []string{"keep"}) {
		t.Fatalf("appendNonEmpty blank = %v", got)
	}
	for _, path := range []string{"/Users/example", "/System/Volumes/Data/Users/example", "/System/Volumes/Data"} {
		_ = firmlinkAlias(path)
	}
	_ = isDeviceFloorException("/dev/fd")
	_ = isDeviceFloorException("/fixture/path")
	_ = EnforcementLine(ResolvedPolicy{Mode: ModeOff})
}

func fuzzSandboxResidualBranches(t *testing.T) {
	t.Helper()
	oldSeatbelt := wrapSeatbelt
	wrapSeatbelt = func(argv []string, _ ResolvedPolicy, _, _ string) ([]string, error) {
		return append([]string{"seatbelt"}, argv...), nil
	}
	w := &Wrapper{policy: ResolvedPolicy{Backend: BackendSeatbelt}}
	if got := w.Wrap([]string{"cmd"}, "/work"); len(got) != 2 || got[0] != "seatbelt" {
		t.Fatalf("seatbelt wrap seam = %v", got)
	}
	wrapSeatbelt = oldSeatbelt

	root := t.TempDir()
	session := filepath.Join(root, "session")
	if err := os.MkdirAll(session, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = buildBwrapArgv(ResolvedPolicy{Spawned: AccessScope{Read: ReadAnywhere, ReadRoots: []string{session}}}, session, root)
	oldEval, oldStat := bwrapEvalSymlinks, bwrapStat
	bwrapEvalSymlinks = func(string) (string, error) { return "/fixture/resolved", nil }
	bwrapStat = func(string) (os.FileInfo, error) { return nil, errors.New("stat") }
	maskInvisible(&[]string{}, map[string]bool{}, "fixture")
	bwrapEvalSymlinks, bwrapStat = oldEval, oldStat

	oldAbs := resolveAbs
	resolveAbs = func(string) (string, error) { return "", errors.New("abs") }
	_, _ = Resolve(SandboxPolicy{Mode: ModeReadOnly}, HostFacts{OS: "linux", Home: "/home", BwrapCapable: true}, "relative")
	resolveAbs = oldAbs
	_, _ = ResolveNamed("read-only", nil, HostFacts{OS: "linux", Home: "relative"}, root, nil)
	_, _ = chooseBackend(SandboxPolicy{Mode: ModeReadOnly}, HostFacts{OS: "plan9"}, true)
	_ = filterMasked([]string{"/secret"}, ResolvedPolicy{MaskedPaths: []string{"/secret"}})
	faultPolicy := ResolvedPolicy{resolveInputs: SandboxPolicy{Mode: ModeReadOnly}, resolveHost: HostFacts{OS: "linux"}}
	_, _ = faultPolicy.ControlPolicy(root)
	_, _ = (&Wrapper{policy: faultPolicy}).ReRoot(root)
	_ = includePathValue("not-a-path-line")
	oldWalk := gitWalkDir
	_ = os.MkdirAll(filepath.Join(root, "modules"), 0o755)
	gitWalkDir = func(root string, fn fs.WalkDirFunc) error { return fn(root, nil, errors.New("walk")) }
	_ = moduleConfigProtections(root)
	gitWalkDir = oldWalk
	nestedConfig := filepath.Join(root, "nested.conf")
	if err := os.WriteFile(nestedConfig, []byte("[include]\npath = *.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootConfig := filepath.Join(root, "root.conf")
	if err := os.WriteFile(rootConfig, []byte("[include]\npath = nested.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _ = scanConfigIncludes(rootConfig, []string{root}, map[string]bool{}, 0)

	oldHome := probeUserHomeDir
	probeUserHomeDir = func() (string, error) { return "/fixture/home", nil }
	if home, err := (hostProbeSystem{}).userHomeDir(); err != nil || home != "/fixture/home" {
		t.Fatalf("home seam = %q, %v", home, err)
	}
	probeUserHomeDir = oldHome
	_ = (RealProber{system: nil}).Probe()
	_ = bwrapSupportsOverlay(scriptedProbeSystem{bwrapHelpErr: errors.New("help")}, "/fixture/bwrap")

	oldTemp, oldCache, oldRead := sessionScratchTempDir, sessionScratchUserCacheDir, sessionScratchReadDir
	sessionScratchTempDir = func() string { return root }
	sessionScratchUserCacheDir = func() (string, error) { return root, nil }
	workspace := filepath.Join(root, "different-workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	tmp, err := NewSessionScratch("", workspace)
	if err != nil {
		t.Fatal(err)
	}
	_ = tmp.Cleanup()
	sessionScratchReadDir = func(string) ([]os.DirEntry, error) { return []os.DirEntry{faultDirEntry{}}, nil }
	sweepCrashedSessionScratch(root)
	sessionScratchTempDir, sessionScratchUserCacheDir, sessionScratchReadDir = oldTemp, oldCache, oldRead
}

type faultDirEntry struct{}

func (faultDirEntry) Name() string               { return sessionScratchPrefix + "fault" }
func (faultDirEntry) IsDir() bool                { return true }
func (faultDirEntry) Type() fs.FileMode          { return fs.ModeDir }
func (faultDirEntry) Info() (fs.FileInfo, error) { return nil, errors.New("info") }

func fuzzEnvHasName(env []string, name string) bool {
	_, ok := fuzzEnvValue(env, name)
	return ok
}

func fuzzEnvValue(env []string, name string) (string, bool) {
	prefix := name + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix), true
		}
	}
	return "", false
}

func hasAssemblySeq(args []string, want ...string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			return true
		}
	}
	return false
}
