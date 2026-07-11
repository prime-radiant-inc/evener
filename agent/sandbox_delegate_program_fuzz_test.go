//go:build serffuzz

package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/sandbox"
)

// FuzzDelegateSandboxPolicyProgram drives the delegate sandbox floor and its
// durable snapshot boundary. It never provisions a wrapper or launches a
// process: the policy decision and snapshot conversion are pure.
//
// Oracles:
//   - valid delegate policies are never less confining than their parent;
//   - network access cannot be escalated; and
//   - snapshots round-trip without aliasing mutable policy inputs.
func FuzzDelegateSandboxPolicyProgram(f *testing.F) {
	for _, seed := range []struct {
		program []byte
		mode    string
		path    string
	}{
		{[]byte{0, 0, 0, 0, 0}, "", "policy"},
		{[]byte{1, 1, 1, 1, 1}, "off", "/opt/secret"},
		{[]byte{2, 2, 2, 2, 2}, "read-only", "relative"},
		{[]byte{3, 3, 3, 3, 3}, "workspace-write", "~/cache"},
		{[]byte{4, 4, 4, 4, 4}, "restricted", "/srv/build"},
		{[]byte{5, 5, 5, 5, 5}, "mistyped", ""},
		{[]byte{0, 0, 0, 1, 2}, "", "net-only-off-parent"},
		{[]byte{1, 0, 0, 1, 2}, "", "net-only-sandboxed-parent"},
		{[]byte{0, 0, 1, 1, 3}, "off", "off-with-net"},
		{[]byte{0, 0, 1, 0, 3}, "off", "off-inherit"},
		{[]byte{3, 1, 4, 2, 1}, "restricted", "network-escalation"},
	} {
		f.Add(seed.program, seed.mode, seed.path)
	}

	f.Fuzz(func(t *testing.T, program []byte, suppliedMode, path string) {
		byteAt := func(index int) byte {
			if index < len(program) {
				return program[index]
			}
			return 0
		}
		allModes := sandbox.AllModes()
		parent := allModes[int(byteAt(0))%len(allModes)]
		parentNet := byteAt(1)&1 == 0
		requestMode := delegateSandboxRequestMode(byteAt(2), suppliedMode)
		requestNet := delegateSandboxRequestNet(byteAt(3))

		first, firstErr := resolveDelegateSandboxRequest(requestMode, requestNet, parent, parentNet)
		second, secondErr := resolveDelegateSandboxRequest(requestMode, requestNet, parent, parentNet)
		if (firstErr == nil) != (secondErr == nil) || !delegateSandboxPolicyEqual(first, second) {
			t.Fatalf("delegate sandbox decision was not deterministic: (%+v, %v) then (%+v, %v)", first, firstErr, second, secondErr)
		}
		if firstErr != nil {
			if !strings.Contains(firstErr.Error(), "invalid_request:") {
				t.Fatalf("sandbox refusal omitted invalid_request: %v", firstErr)
			}
		} else if first != nil {
			if !first.Mode.AtLeastAsConfining(parent) {
				t.Fatalf("delegate mode %s escalates parent mode %s", first.Mode, parent)
			}
			if first.Network == nil {
				t.Fatal("non-off delegate policy omitted concrete network decision")
			}
			if !parentNet && *first.Network {
				t.Fatal("delegate escalated a network-off parent")
			}
			if len(first.DenylistAdd) != 0 || len(first.DenylistRemove) != 0 || len(first.ExtraReadRoots) != 0 || len(first.ExtraWritableRoots) != 0 {
				t.Fatalf("delegate policy unexpectedly originated non-mode inputs: %+v", first)
			}
		}
		if strings.TrimSpace(requestMode) == "" && requestNet == nil && (firstErr != nil || first != nil) {
			t.Fatalf("absent sandbox request = (%+v, %v), want inherit", first, firstErr)
		}

		// The recovery hint must enumerate exactly the non-escalating modes.
		allowed := allowedDelegateModes(parent)
		for _, name := range strings.Split(allowed, ", ") {
			mode, err := sandbox.ParseMode(name)
			if err != nil || !mode.AtLeastAsConfining(parent) {
				t.Fatalf("allowed mode %q is not valid under %s", name, parent)
			}
		}
		for _, mode := range allModes {
			contains := strings.Contains(", "+allowed+", ", ", "+mode.String()+", ")
			if contains != mode.AtLeastAsConfining(parent) {
				t.Fatalf("allowed mode set %q disagrees for %s under %s", allowed, mode, parent)
			}
		}

		delegateSandboxSnapshotContract(t, byteAt(4), path)
		delegateSandboxParentContract(t, parent, parentNet)
	})
}

func delegateSandboxRequestMode(selector byte, supplied string) string {
	switch selector % 6 {
	case 0:
		return ""
	case 1:
		return "off"
	case 2:
		return "read-only"
	case 3:
		return "workspace-write"
	case 4:
		return "restricted"
	default:
		return strings.TrimSpace(supplied)
	}
}

func delegateSandboxRequestNet(selector byte) *bool {
	switch selector % 3 {
	case 0:
		return nil
	case 1:
		value := false
		return &value
	default:
		value := true
		return &value
	}
}

func delegateSandboxPolicyEqual(a, b *sandbox.SandboxPolicy) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Mode != b.Mode || (a.Network == nil) != (b.Network == nil) {
		return false
	}
	return a.Network == nil || *a.Network == *b.Network
}

func delegateSandboxSnapshotContract(t *testing.T, selector byte, path string) {
	t.Helper()
	path = strings.TrimSpace(path)
	if path == "" {
		path = "policy"
	}
	if len(path) > 120 {
		path = path[:120]
	}
	modes := sandbox.AllModes()
	value := selector&1 == 0
	input := sandbox.SandboxPolicy{
		Mode:               modes[int(selector)%len(modes)],
		Network:            &value,
		DenylistAdd:        []string{path},
		DenylistRemove:     []string{"remove-" + path},
		ExtraWritableRoots: []string{"write-" + path},
		ExtraReadRoots:     []string{"read-" + path},
	}
	snap := sandboxSnapshotFromInputs(input)
	if input.Mode == sandbox.ModeOff {
		if snap != nil {
			t.Fatalf("off policy produced durable snapshot: %+v", snap)
		}
		return
	}
	if snap == nil {
		t.Fatal("sandboxed policy lost its snapshot")
	}
	if snap.Mode != input.Mode.String() || snap.Network == nil || *snap.Network != value {
		t.Fatalf("snapshot lost mode or network: %+v", snap)
	}
	if !slices.Equal(snap.DenylistAdd, input.DenylistAdd) || !slices.Equal(snap.DenylistRemove, input.DenylistRemove) || !slices.Equal(snap.ExtraWritableRoots, input.ExtraWritableRoots) || !slices.Equal(snap.ExtraReadRoots, input.ExtraReadRoots) {
		t.Fatalf("snapshot lost policy axes: %+v", snap)
	}
	input.DenylistAdd[0] = "mutated-input"
	if snap.DenylistAdd[0] == "mutated-input" {
		t.Fatal("snapshot aliases policy input")
	}
	clone := cloneSandboxSnapshot(snap)
	if clone == nil || !delegateSandboxSnapshotEqual(clone, snap) {
		t.Fatalf("snapshot clone differs: clone=%+v original=%+v", clone, snap)
	}
	clone.DenylistAdd[0] = "mutated-clone"
	if snap.DenylistAdd[0] == "mutated-clone" {
		t.Fatal("snapshot clone aliases original")
	}
	if clone.Network != nil {
		*clone.Network = !*clone.Network
		if snap.Network != nil && *clone.Network == *snap.Network {
			t.Fatal("snapshot clone aliases network pointer")
		}
	}
	if cloneSandboxSnapshot(nil) != nil {
		t.Fatal("nil snapshot clone was non-nil")
	}
	restored, ok := sandboxPolicyFromSnapshot(snap)
	if !ok || restored.Mode != input.Mode || restored.Network == nil || *restored.Network != value {
		t.Fatalf("snapshot did not round-trip: policy=%+v ok=%v", restored, ok)
	}
	if _, ok := sandboxPolicyFromSnapshot(nil); ok {
		t.Fatal("nil snapshot was accepted")
	}
	if _, ok := sandboxPolicyFromSnapshot(&jobstore.SandboxSnapshot{Mode: "not-a-mode"}); ok {
		t.Fatal("invalid snapshot mode was accepted")
	}
}

func delegateSandboxSnapshotEqual(a, b *jobstore.SandboxSnapshot) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Mode != b.Mode || (a.Network == nil) != (b.Network == nil) {
		return false
	}
	if a.Network != nil && *a.Network != *b.Network {
		return false
	}
	return slices.Equal(a.DenylistAdd, b.DenylistAdd) &&
		slices.Equal(a.DenylistRemove, b.DenylistRemove) &&
		slices.Equal(a.ExtraWritableRoots, b.ExtraWritableRoots) &&
		slices.Equal(a.ExtraReadRoots, b.ExtraReadRoots)
}

func delegateSandboxParentContract(t *testing.T, mode sandbox.Mode, network bool) {
	t.Helper()
	plain := &Session{env: &agenttest.DenyEnv{}}
	if gotMode, gotNet := plain.parentSandboxModeNet(); gotMode != sandbox.ModeOff || !gotNet {
		t.Fatalf("non-local parent sandbox = (%s,%v), want off/on", gotMode, gotNet)
	}
	local := execenv.NewLocalExecutionEnvironment(t.TempDir())
	if snap := sandboxSnapshotFromEnv(local); snap != nil {
		t.Fatalf("unsandboxed local env produced snapshot: %+v", snap)
	}
	local.Sandbox = &sandbox.ResolvedPolicy{Mode: mode, Network: network}
	session := &Session{env: local}
	wantNetwork := network
	if mode == sandbox.ModeOff {
		wantNetwork = true
	}
	if gotMode, gotNet := session.parentSandboxModeNet(); gotMode != mode || gotNet != wantNetwork {
		t.Fatalf("local parent sandbox = (%s,%v), want (%s,%v)", gotMode, gotNet, mode, wantNetwork)
	}
	if err := provisionRestoredSandbox(SessionConfig{}, &agenttest.DenyEnv{}); err != nil {
		t.Fatalf("off sandbox restore returned error: %v", err)
	}
	if err := provisionRestoredSandbox(SessionConfig{Sandbox: "restricted"}, &agenttest.DenyEnv{}); err == nil {
		t.Fatal("non-local sandbox restore was accepted")
	}
	delegateSandboxProvisionContract(t)
}

func delegateSandboxProvisionContract(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("create synthetic git dir: %v", err)
	}
	local := execenv.NewLocalExecutionEnvironment(root)
	defer local.Cleanup()
	cfg := SessionConfig{Sandbox: "restricted"}
	cfg.testOnly.sandboxProber = sandbox.FakeProber{Facts: sandbox.HostFacts{
		OS:               "linux",
		Home:             t.TempDir(),
		BwrapPath:        "/fixture/bwrap",
		BwrapCapable:     true,
		OverlaySupported: true,
	}}
	if err := provisionRestoredSandbox(cfg, local); err != nil {
		t.Fatalf("provision restored sandbox: %v", err)
	}
	if local.Sandbox == nil || !local.Sandbox.Enforced() || local.Wrapper == nil {
		t.Fatalf("sandbox provision did not install full policy: sandbox=%v wrapper=%v", local.Sandbox, local.Wrapper)
	}
	snap := sandboxSnapshotFromEnv(local)
	if snap == nil || snap.Mode != sandbox.ModeRestricted.String() {
		t.Fatalf("sandbox env snapshot = %+v, want restricted policy", snap)
	}
	badResolve := SessionConfig{Sandbox: "mistyped"}
	badResolve.testOnly.sandboxProber = cfg.testOnly.sandboxProber
	if err := provisionRestoredSandbox(badResolve, execenv.NewLocalExecutionEnvironment(root)); err == nil {
		t.Fatal("invalid persisted sandbox mode was accepted")
	}
	badWrapper := SessionConfig{Sandbox: "restricted"}
	badWrapper.testOnly.sandboxProber = sandbox.FakeProber{Facts: sandbox.HostFacts{
		OS: "linux", Home: t.TempDir(), BwrapPath: "relative/bwrap", BwrapCapable: true,
	}}
	if err := provisionRestoredSandbox(badWrapper, execenv.NewLocalExecutionEnvironment(root)); err == nil {
		t.Fatal("relative sandbox backend path was accepted")
	}
}
