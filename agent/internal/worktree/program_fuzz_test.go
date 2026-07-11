//go:build serffuzz

package worktree

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzWorktreePredicatesProgram runs the cleanup and merge predicates through
// a scripted GitRunner. The runner is the package's external boundary, so the
// program exercises real decision code without invoking Git or inspecting a
// checkout.
func FuzzWorktreePredicatesProgram(f *testing.F) {
	f.Add("lane", "tip-sha", "base-sha")
	f.Add("release/v2", "tip", "base")

	f.Fuzz(func(t *testing.T, target, tip, base string) {
		if target == "" {
			target = "main"
		}
		if tip == "" {
			tip = "tip"
		}
		if base == "" {
			base = "base"
		}

		clean, offending, err := CleanTree(func(args ...string) (string, error) {
			return "", nil
		}, "/fixture/worktree")
		if err != nil || !clean || len(offending) != 0 {
			t.Fatalf("CleanTree clean = %v, %#v, %v", clean, offending, err)
		}
		clean, offending, err = CleanTree(func(args ...string) (string, error) {
			return " M changed.go\n?? new.txt\n", nil
		}, "/fixture/worktree")
		if err != nil || clean || !reflectStringSlicesEqual(offending, []string{" M changed.go", "?? new.txt"}) {
			t.Fatalf("CleanTree dirty = %v, %#v, %v", clean, offending, err)
		}
		if _, _, err := CleanTree(func(args ...string) (string, error) {
			return "", errors.New("status fault")
		}, "/fixture/worktree"); err == nil {
			t.Fatal("CleanTree status failure did not propagate")
		}

		unchanged, err := Unchanged(func(args ...string) (string, error) {
			switch args[2] {
			case "status":
				return "", nil
			case "rev-parse":
				return base + "\n", nil
			default:
				return "", errors.New("unexpected command")
			}
		}, "/fixture/worktree", base)
		if err != nil || !unchanged {
			t.Fatalf("Unchanged equal clean = %v, %v", unchanged, err)
		}
		unchanged, err = Unchanged(func(args ...string) (string, error) {
			return " M changed.go\n", nil
		}, "/fixture/worktree", base)
		if err != nil || unchanged {
			t.Fatalf("Unchanged dirty = %v, %v", unchanged, err)
		}
		if _, err := Unchanged(func(args ...string) (string, error) {
			if len(args) > 2 && args[2] == "status" {
				return "", nil
			}
			return "", errors.New("head fault")
		}, "/fixture/worktree", base); err == nil {
			t.Fatal("Unchanged head failure did not propagate")
		}
		unchanged, err = Unchanged(func(args ...string) (string, error) {
			if len(args) > 2 && args[2] == "status" {
				return "", nil
			}
			return "other-tip\n", nil
		}, "/fixture/worktree", base)
		if err != nil || unchanged {
			t.Fatalf("Unchanged moved tip = %v, %v", unchanged, err)
		}
		if _, err := Unchanged(func(args ...string) (string, error) {
			return "", errors.New("status fault")
		}, "/fixture/worktree", base); err == nil {
			t.Fatal("Unchanged status failure did not propagate")
		}

		if sha, ok := resolveRef(func(args ...string) (string, error) { return tip + "\n", nil }, "refs/heads/"+target); !ok || sha != tip {
			t.Fatalf("resolveRef success = %q, %v", sha, ok)
		}
		if _, ok := resolveRef(func(args ...string) (string, error) { return "", errors.New("missing") }, "refs/heads/"+target); ok {
			t.Fatal("resolveRef missing = ok")
		}
		tips, err := remoteTrackingTips(func(args ...string) (string, error) {
			return "malformed\nrefs/remotes/origin/" + target + " remote-tip\n", nil
		}, target)
		if err != nil || len(tips) != 1 || tips[0].sha != "remote-tip" {
			t.Fatalf("remoteTrackingTips = %#v, %v", tips, err)
		}
		if _, err := remoteTrackingTips(func(args ...string) (string, error) { return "", errors.New("remote fault") }, target); err == nil {
			t.Fatal("remoteTrackingTips fault did not propagate")
		}

		ancestor, err := isAncestor(func(args ...string) (string, error) { return "", nil }, tip, base)
		if err != nil || !ancestor {
			t.Fatalf("isAncestor true = %v, %v", ancestor, err)
		}
		ancestor, err = isAncestor(func(args ...string) (string, error) { return "", worktreeProgramExitError(1) }, tip, base)
		if err != nil || ancestor {
			t.Fatalf("isAncestor negative = %v, %v", ancestor, err)
		}
		if _, err := isAncestor(func(args ...string) (string, error) { return "", worktreeProgramExitError(128) }, tip, base); err == nil {
			t.Fatal("isAncestor genuine failure did not propagate")
		}
		behind, err := isBehind(func(args ...string) (string, error) { return "", nil }, tip, tip)
		if err != nil || behind {
			t.Fatalf("isBehind equal = %v, %v", behind, err)
		}

		cherry, err := cherryEquivalent(func(args ...string) (string, error) { return "- patch-one\n- patch-two\n", nil }, "target", tip, base)
		if err != nil || !cherry {
			t.Fatalf("cherryEquivalent all equivalent = %v, %v", cherry, err)
		}
		cherry, err = cherryEquivalent(func(args ...string) (string, error) { return "+ unique\n", nil }, "target", tip, base)
		if err != nil || cherry {
			t.Fatalf("cherryEquivalent unique = %v, %v", cherry, err)
		}
		cherry, err = cherryEquivalent(func(args ...string) (string, error) { return "", nil }, "target", tip, base)
		if err != nil || cherry {
			t.Fatalf("cherryEquivalent empty = %v, %v", cherry, err)
		}
		if _, err := cherryEquivalent(func(args ...string) (string, error) { return "", errors.New("cherry fault") }, "target", tip, base); err == nil {
			t.Fatal("cherryEquivalent fault did not propagate")
		}

		merged, arm, err := checkMerged(func(args ...string) (string, error) {
			if args[0] == "merge-base" {
				return "", worktreeProgramExitError(1)
			}
			return "- equivalent\n", nil
		}, tip, "target", base)
		if err != nil || !merged || arm != "cherry" {
			t.Fatalf("checkMerged cherry = %v, %q, %v", merged, arm, err)
		}
		merged, arm, err = checkMerged(func(args ...string) (string, error) {
			return "", nil
		}, tip, "target", base)
		if err != nil || !merged || arm != "ancestry" {
			t.Fatalf("checkMerged ancestry = %v, %q, %v", merged, arm, err)
		}
		merged, arm, err = checkMerged(func(args ...string) (string, error) {
			if args[0] == "merge-base" {
				return "", worktreeProgramExitError(1)
			}
			return "+ unique\n", nil
		}, tip, "target", base)
		if err != nil || merged || arm != "" {
			t.Fatalf("checkMerged unmerged = %v, %q, %v", merged, arm, err)
		}
		if _, _, err := checkMerged(func(args ...string) (string, error) {
			return "", errors.New("merge-base fault")
		}, tip, "target", base); err == nil {
			t.Fatal("checkMerged ancestry fault did not propagate")
		}
		if _, _, err := checkMerged(func(args ...string) (string, error) {
			if args[0] == "merge-base" {
				return "", worktreeProgramExitError(1)
			}
			return "", errors.New("cherry fault")
		}, tip, "target", base); err == nil {
			t.Fatal("checkMerged cherry fault did not propagate")
		}

		assertWorktreeMergedPrograms(t, target, tip, base)
		assertWorktreeDecisionAndVersionPrograms(t, target)
		if !Adopted("moved", base, "removed") || Adopted(base, base, "removed") || Adopted("removed", base, "removed") {
			t.Fatal("Adopted two-SHA rule violated")
		}
	})
}

type worktreeProgramExitError int

func (e worktreeProgramExitError) Error() string { return "scripted git exit" }
func (e worktreeProgramExitError) ExitCode() int { return int(e) }

func reflectStringSlicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func assertWorktreeMergedPrograms(t *testing.T, target, tip, base string) {
	t.Helper()
	unknown, err := Merged(func(args ...string) (string, error) {
		return "", errors.New("missing")
	}, tip, "", base)
	if err != nil || !unknown.TargetUnknown || unknown.Merged {
		t.Fatalf("Merged empty target = %#v, %v", unknown, err)
	}

	ancestry, err := Merged(func(args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return "target-tip\n", nil
		case "for-each-ref":
			return "", nil
		case "merge-base":
			return "", nil
		default:
			return "", errors.New("unexpected command")
		}
	}, tip, target, base)
	if err != nil || !ancestry.Merged || ancestry.Arm != "ancestry" || ancestry.TargetRef != "refs/heads/"+target {
		t.Fatalf("Merged ancestry = %#v, %v", ancestry, err)
	}

	remoteOnly, err := Merged(func(args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return "", errors.New("missing local")
		case "for-each-ref":
			return "refs/remotes/origin/" + target + " remote-tip\n", nil
		case "merge-base":
			return "", nil
		default:
			return "", errors.New("unexpected command")
		}
	}, tip, target, base)
	if err != nil || !remoteOnly.Merged || remoteOnly.TargetRef != "refs/remotes/origin/"+target {
		t.Fatalf("Merged remote-only = %#v, %v", remoteOnly, err)
	}

	remoteAhead, err := Merged(func(args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return "local-tip\n", nil
		case "for-each-ref":
			return "refs/remotes/origin/" + target + " remote-tip\n", nil
		case "merge-base":
			if args[2] == "local-tip" {
				return "", nil // local target is behind the remote tracking tip
			}
			if args[3] == "local-tip" {
				return "", worktreeProgramExitError(1)
			}
			return "", nil // remote candidate contains the worktree tip
		case "cherry":
			return "+ unique\n", nil
		default:
			return "", errors.New("unexpected command")
		}
	}, tip, target, base)
	if err != nil || !remoteAhead.Merged || remoteAhead.TargetRef != "refs/remotes/origin/"+target {
		t.Fatalf("Merged remote-ahead = %#v, %v", remoteAhead, err)
	}

	noRefs, err := Merged(func(args ...string) (string, error) {
		if args[0] == "for-each-ref" {
			return "", nil
		}
		return "", errors.New("missing")
	}, tip, target, base)
	if err != nil || !noRefs.TargetUnknown || noRefs.Merged {
		t.Fatalf("Merged no refs = %#v, %v", noRefs, err)
	}
	if _, err := Merged(func(args ...string) (string, error) {
		if args[0] == "for-each-ref" {
			return "", errors.New("remote lookup fault")
		}
		return "target-tip\n", nil
	}, tip, target, base); err == nil {
		t.Fatal("Merged remote lookup fault did not propagate")
	}
	notMerged, err := Merged(func(args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return "target-tip\n", nil
		case "for-each-ref":
			return "", nil
		case "merge-base":
			return "", worktreeProgramExitError(1)
		case "cherry":
			return "+ unique\n", nil
		default:
			return "", errors.New("unexpected command")
		}
	}, tip, target, base)
	if err != nil || notMerged.Merged || notMerged.TargetRef != "refs/heads/"+target {
		t.Fatalf("Merged unmerged local candidate = %#v, %v", notMerged, err)
	}
}

// FuzzSidecarLifecycleProgram drives the persisted metadata lifecycle through
// the real filesystem API under a test-owned temporary root.
func FuzzSidecarLifecycleProgram(f *testing.F) {
	f.Add("base", "session", "2026-01-01T00:00:00Z", true)
	f.Add("", "", "", false)

	f.Fuzz(func(t *testing.T, base, session, createdAt string, removed bool) {
		dir := t.TempDir()
		sc := Sidecar{
			Name:            "lane",
			Branch:          "lane",
			BaseSHA:         base,
			MergeTarget:     "main",
			OriginalRoot:    "/fixture/root",
			CreatorSession:  session,
			WorktreeRemoved: removed,
			CreatedAt:       createdAt,
		}
		want := worktreeProgramJSONSidecar(t, sc)
		if err := WriteSidecarExcl(dir, sc.Name, sc); err != nil {
			t.Fatalf("WriteSidecarExcl: %v", err)
		}
		if err := WriteSidecarExcl(dir, sc.Name, sc); !os.IsExist(err) {
			t.Fatalf("second WriteSidecarExcl = %v, want os.IsExist", err)
		}
		got, err := ReadSidecar(dir, sc.Name)
		if err != nil || got != want {
			t.Fatalf("ReadSidecar = %#v, %v; want %#v", got, err, want)
		}
		want.WorktreeRemoved = true
		want.TipSHAAtRemoval = "removed-tip"
		if err := UpdateSidecar(dir, sc.Name, func(updated *Sidecar) {
			updated.WorktreeRemoved = true
			updated.TipSHAAtRemoval = "removed-tip"
		}); err != nil {
			t.Fatalf("UpdateSidecar: %v", err)
		}
		updated, err := ReadSidecar(dir, sc.Name)
		if err != nil || updated != want {
			t.Fatalf("updated sidecar = %#v, %v; want %#v", updated, err, want)
		}

		if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("noise"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "bad%.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "garbage.json"), []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "directory.json"), 0o755); err != nil {
			t.Fatal(err)
		}
		listed, err := ListSidecars(dir)
		if err != nil || len(listed) != 1 || listed[0].Name != sc.Name || listed[0].TipSHAAtRemoval != "removed-tip" {
			t.Fatalf("ListSidecars = %#v, %v", listed, err)
		}
		if err := DeleteSidecar(dir, sc.Name); err != nil {
			t.Fatalf("DeleteSidecar: %v", err)
		}
		if _, err := ReadSidecar(dir, sc.Name); !os.IsNotExist(err) {
			t.Fatalf("ReadSidecar after delete = %v, want os.IsNotExist", err)
		}
		if err := UpdateSidecar(dir, sc.Name, func(*Sidecar) {}); !os.IsNotExist(err) {
			t.Fatalf("UpdateSidecar missing = %v, want os.IsNotExist", err)
		}
		if err := DeleteSidecar(dir, sc.Name); !os.IsNotExist(err) {
			t.Fatalf("DeleteSidecar missing = %v, want os.IsNotExist", err)
		}
		if _, err := ListSidecars(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
			t.Fatalf("ListSidecars missing = %v, want os.IsNotExist", err)
		}
		if _, err := SidecarAge(dir, sc.Name); !os.IsNotExist(err) {
			t.Fatalf("SidecarAge missing = %v, want os.IsNotExist", err)
		}
	})
}

// worktreeProgramJSONSidecar returns the logical Sidecar value persisted by
// encoding/json. Go strings may contain invalid UTF-8, which JSON replaces with
// U+FFFD during encoding; the lifecycle fuzzer intentionally keeps those byte
// strings in scope and compares against this canonical persisted value.
func worktreeProgramJSONSidecar(t *testing.T, sc Sidecar) Sidecar {
	t.Helper()
	raw, err := json.Marshal(sc)
	if err != nil {
		t.Fatalf("marshal expected sidecar: %v", err)
	}
	var want Sidecar
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("unmarshal expected sidecar: %v", err)
	}
	return want
}

func assertWorktreeDecisionAndVersionPrograms(t *testing.T, target string) {
	t.Helper()
	events := []LockEvent{
		EvCreate, EvLeave, EvEnter, EvEnterCurrent, EvRestoreLand, EvInitInside,
		EvResumeReenter, EvRemoveTarget, EvRemoveCurrent, EvDelegateCreate,
		EvDelegateRevive, EvDisposeUnchanged, EvDisposeChanged, EvPruneCandidate,
	}
	states := []LockState{Unlocked, OwnSession, OwnDelegate, Foreign}
	for _, event := range events {
		for _, state := range states {
			action := Decide(event, state)
			if action < ActNone || action > lastAction {
				t.Fatalf("Decide(%v, %v) = %v outside action range", event, state, action)
			}
			_ = event.String()
			_ = state.String()
			_ = action.String()
		}
		if got := Decide(event, LockState(-1)); got != ActRefuse {
			t.Fatalf("Decide(%v, unknown) = %v, want ActRefuse", event, got)
		}
	}
	if Decide(LockEvent(-1), LockState(-1)) != ActRefuse || LockEvent(-1).String() != "LockEvent(?)" || LockState(-1).String() != "LockState(?)" || LockAction(-1).String() != "LockAction(?)" {
		t.Fatal("unknown lock values must fail safe and render as unknown")
	}
	for _, tc := range []struct {
		reason string
		want   LockState
	}{
		{"", Foreign},
		{"serf:session", OwnSession},
		{"serf:dlg:delegate:session", OwnDelegate},
		{"serf:other-session", Foreign},
		{"serf:dlg:other-delegate:session", Foreign},
		{"held by another tool", Foreign},
	} {
		if got := ClassifyReason(tc.reason, "session", "delegate"); got != tc.want {
			t.Fatalf("ClassifyReason(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
	if err := ValidateName("a..b"); err == nil {
		t.Fatal("ValidateName accepted a double-dot component")
	}
	if got := CUnquote(`"x\q"`); got != `x\q` {
		t.Fatalf("CUnquote unknown escape = %q", got)
	}

	for _, root := range []string{"/fixture/" + target, "/fixture/name with space", "/", "/fixture/" + strings.Repeat("x", maxBasenameBytes+8)} {
		id := ProjectID(root)
		if id == "" || len(id) < 17 {
			t.Fatalf("ProjectID(%q) = %q", root, id)
		}
	}
	if err := CheckGitVersion(func(args ...string) (string, error) { return "git version 2.33.0\n", nil }); err != nil {
		t.Fatalf("supported CheckGitVersion: %v", err)
	}
	for _, run := range []GitRunner{
		func(args ...string) (string, error) { return "git version 2.32.9\n", nil },
		func(args ...string) (string, error) { return "not a git banner", nil },
		func(args ...string) (string, error) { return "", errors.New("git unavailable") },
	} {
		if err := CheckGitVersion(run); err == nil {
			t.Fatal("unsupported CheckGitVersion input succeeded")
		}
	}
}
