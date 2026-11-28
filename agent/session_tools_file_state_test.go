package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

func execSessionTool(t *testing.T, s *Session, id, name string, args map[string]any) tool.ExecResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return s.execTool(t.Context(), llm.ToolCallData{ID: id, Name: name, Arguments: raw}, "")
}

func TestWriteFileTracksReadState(t *testing.T) {
	t.Parallel()

	t.Run("new file write suppresses warning on subsequent edit and write", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		s := newSession(t, withDir(dir))

		res := execSessionTool(t, s, "w1", "write_file", map[string]any{
			"file_path": "fresh.txt",
			"content":   "alpha\nbeta\n",
		})
		if res.IsError {
			t.Fatalf("write_file failed: %s", res.Output)
		}
		if s.readBeforeWriteWarning("fresh.txt") != "" {
			t.Fatalf("write_file did not track newly created file")
		}

		res = execSessionTool(t, s, "e1", "edit_file", map[string]any{
			"file_path": "fresh.txt",
			"edits":     []any{map[string]any{"old_string": "alpha", "new_string": "ALPHA"}},
		})
		if res.IsError {
			t.Fatalf("edit_file failed: %s", res.Output)
		}
		if strings.Contains(res.Output, "WARNING:") {
			t.Fatalf("unexpected unread warning on edit_file after write_file: %q", res.Output)
		}

		res = execSessionTool(t, s, "w2", "write_file", map[string]any{
			"file_path": "fresh.txt",
			"content":   "rewritten\n",
		})
		if res.IsError {
			t.Fatalf("second write_file failed: %s", res.Output)
		}
		if strings.Contains(res.Output, "WARNING:") {
			t.Fatalf("unexpected unread warning on second write_file: %q", res.Output)
		}
	})

	t.Run("pre-existing unread file warns on first write only", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "pre.txt"), []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := newSession(t, withDir(dir))

		first := execSessionTool(t, s, "w1", "write_file", map[string]any{
			"file_path": "pre.txt",
			"content":   "first\n",
		})
		if first.IsError {
			t.Fatalf("first write_file failed: %s", first.Output)
		}
		if !strings.Contains(first.Output, "WARNING:") {
			t.Fatalf("expected unread warning on first write to pre-existing file, got: %q", first.Output)
		}

		second := execSessionTool(t, s, "w2", "write_file", map[string]any{
			"file_path": "pre.txt",
			"content":   "second\n",
		})
		if second.IsError {
			t.Fatalf("second write_file failed: %s", second.Output)
		}
		if strings.Contains(second.Output, "WARNING:") {
			t.Fatalf("unexpected repeat warning on second write_file: %q", second.Output)
		}
	})

	t.Run("failed write does not track path", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "locked.txt"), []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := newSession(t, withDir(dir))
		local := s.currentEnv().(*execenv.LocalExecutionEnvironment)
		local.Sandbox = &sandbox.ResolvedPolicy{
			Mode:     sandbox.ModeReadOnly,
			FileTool: sandbox.AccessScope{Read: sandbox.ReadWorktreeOnly, ReadRoots: []string{dir}},
		}

		failed := execSessionTool(t, s, "w-fail", "write_file", map[string]any{
			"file_path": "locked.txt",
			"content":   "denied\n",
		})
		if !failed.IsError {
			t.Fatalf("expected read-only write_file to fail, got: %q", failed.Output)
		}
		if s.readBeforeWriteWarning("locked.txt") == "" {
			t.Fatal("failed write_file tracked path as read")
		}
	})
}

func TestEditFileTracksReadState(t *testing.T) {
	t.Parallel()

	t.Run("first edit on unread file warns, second edit does not", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "pre.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := newSession(t, withDir(dir))

		first := execSessionTool(t, s, "e1", "edit_file", map[string]any{
			"file_path": "pre.txt",
			"edits":     []any{map[string]any{"old_string": "one", "new_string": "ONE"}},
		})
		if first.IsError {
			t.Fatalf("first edit_file failed: %s", first.Output)
		}
		if !strings.Contains(first.Output, "WARNING:") {
			t.Fatalf("expected unread warning on first edit_file, got: %q", first.Output)
		}
		if s.readBeforeWriteWarning("pre.txt") != "" {
			t.Fatal("successful edit_file did not track path as known")
		}

		second := execSessionTool(t, s, "e2", "edit_file", map[string]any{
			"file_path": "pre.txt",
			"edits":     []any{map[string]any{"old_string": "two", "new_string": "TWO"}},
		})
		if second.IsError {
			t.Fatalf("second edit_file failed: %s", second.Output)
		}
		if strings.Contains(second.Output, "WARNING:") {
			t.Fatalf("unexpected repeat warning on second edit_file: %q", second.Output)
		}
	})

	t.Run("failed edit does not track path", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "pre.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := newSession(t, withDir(dir))

		failed := execSessionTool(t, s, "e-fail", "edit_file", map[string]any{
			"file_path": "pre.txt",
			"edits":     []any{map[string]any{"old_string": "absent", "new_string": "X"}},
		})
		if !failed.IsError {
			t.Fatalf("expected edit_file with missing old_string to fail, got: %q", failed.Output)
		}
		if s.readBeforeWriteWarning("pre.txt") == "" {
			t.Fatal("failed edit_file tracked path as read")
		}

		next := execSessionTool(t, s, "e-ok", "edit_file", map[string]any{
			"file_path": "pre.txt",
			"edits":     []any{map[string]any{"old_string": "one", "new_string": "ONE"}},
		})
		if next.IsError {
			t.Fatalf("edit_file failed: %s", next.Output)
		}
		if !strings.Contains(next.Output, "WARNING:") {
			t.Fatalf("expected unread warning after earlier failed edit_file, got: %q", next.Output)
		}
	})
}

func TestApplyPatchTracksReadAndMismatchState(t *testing.T) {
	t.Parallel()

	t.Run("add file suppresses unread warning on subsequent update, edit, and write", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		s := newSession(t, withDir(dir))

		add := execSessionTool(t, s, "p1", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Add File: created.txt\n+alpha\n+beta\n*** End Patch\n",
		})
		if add.IsError {
			t.Fatalf("apply_patch add failed: %s", add.Output)
		}
		if s.readBeforeWriteWarning("created.txt") != "" {
			t.Fatal("apply_patch Add did not track created file")
		}

		upd := execSessionTool(t, s, "p2", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: created.txt\n@@\n alpha\n-beta\n+BETA\n*** End Patch\n",
		})
		if upd.IsError {
			t.Fatalf("apply_patch update failed: %s", upd.Output)
		}
		if strings.Contains(upd.Output, "WARNING:") {
			t.Fatalf("unexpected unread warning on apply_patch update: %q", upd.Output)
		}

		ed := execSessionTool(t, s, "e1", "edit_file", map[string]any{
			"file_path": "created.txt",
			"edits":     []any{map[string]any{"old_string": "alpha", "new_string": "ALPHA"}},
		})
		if ed.IsError {
			t.Fatalf("edit_file after apply_patch failed: %s", ed.Output)
		}
		if strings.Contains(ed.Output, "WARNING:") {
			t.Fatalf("unexpected unread warning on edit_file after apply_patch Add: %q", ed.Output)
		}
	})

	t.Run("mismatch on unchanged file reports unchanged after read, write, edit, or patch", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name  string
			setup func(t *testing.T, s *Session, dir string)
		}{
			{
				name: "after_read_file",
				setup: func(t *testing.T, s *Session, dir string) {
					if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
						t.Fatal(err)
					}
					if res := execSessionTool(t, s, "r1", "read_file", map[string]any{"file_path": "f.txt"}); res.IsError {
						t.Fatalf("read_file: %s", res.Output)
					}
				},
			},
			{
				name: "after_write_file",
				setup: func(t *testing.T, s *Session, _ string) {
					if res := execSessionTool(t, s, "w1", "write_file", map[string]any{"file_path": "f.txt", "content": "alpha\nbeta\n"}); res.IsError {
						t.Fatalf("write_file: %s", res.Output)
					}
				},
			},
			{
				name: "after_edit_file",
				setup: func(t *testing.T, s *Session, dir string) {
					if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha\nold\n"), 0o644); err != nil {
						t.Fatal(err)
					}
					if res := execSessionTool(t, s, "e1", "edit_file", map[string]any{
						"file_path": "f.txt",
						"edits":     []any{map[string]any{"old_string": "old", "new_string": "beta"}},
					}); res.IsError {
						t.Fatalf("edit_file: %s", res.Output)
					}
				},
			},
			{
				name: "after_apply_patch_add",
				setup: func(t *testing.T, s *Session, _ string) {
					if res := execSessionTool(t, s, "p0", "apply_patch", map[string]any{
						"patch": "*** Begin Patch\n*** Add File: f.txt\n+alpha\n+beta\n*** End Patch\n",
					}); res.IsError {
						t.Fatalf("apply_patch add: %s", res.Output)
					}
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				s := newSession(t, withDir(dir))
				tc.setup(t, s, dir)

				res := execSessionTool(t, s, "p-miss", "apply_patch", map[string]any{
					"patch": "*** Begin Patch\n*** Update File: f.txt\n@@\n alpha\n-wrong\n+gamma\n*** End Patch\n",
				})
				if !res.IsError {
					t.Fatal("expected patch mismatch error")
				}
				mm, ok := errors.AsType[*tool.PatchMismatchError](res.Err)
				if !ok {
					t.Fatalf("err = %T (%v), want *tool.PatchMismatchError", res.Err, res.Err)
				}
				if mm.FileState != tool.PatchFileStateUnchanged {
					t.Fatalf("FileState = %v, want PatchFileStateUnchanged (output:\n%s)", mm.FileState, res.Output)
				}
				if !strings.Contains(res.Output, "the file has not changed") || strings.Contains(res.Output, "the file has changed") {
					t.Fatalf("unexpected mismatch output for unchanged file:\n%s", res.Output)
				}
			})
		}
	})

	t.Run("mismatch on externally modified file reports changed", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "f.txt")
		if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := newSession(t, withDir(dir))
		if res := execSessionTool(t, s, "r1", "read_file", map[string]any{"file_path": "f.txt"}); res.IsError {
			t.Fatalf("read_file: %s", res.Output)
		}
		// Externally modify content (different size).
		if err := os.WriteFile(path, []byte("alpha\nbeta\nexternal\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		res := execSessionTool(t, s, "p-miss", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: f.txt\n@@\n alpha\n-wrong\n+gamma\n*** End Patch\n",
		})
		if !res.IsError {
			t.Fatal("expected patch mismatch error")
		}
		mm, ok := errors.AsType[*tool.PatchMismatchError](res.Err)
		if !ok {
			t.Fatalf("err = %T (%v), want *tool.PatchMismatchError", res.Err, res.Err)
		}
		if mm.FileState != tool.PatchFileStateChanged {
			t.Fatalf("FileState = %v, want PatchFileStateChanged (output:\n%s)", mm.FileState, res.Output)
		}
		if !strings.Contains(res.Output, "the file has changed") {
			t.Fatalf("expected changed-file hint in output:\n%s", res.Output)
		}
	})

	t.Run("mismatch on never read or written file stays neutral", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "unseen.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := newSession(t, withDir(dir))

		res := execSessionTool(t, s, "p-miss", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: unseen.txt\n@@\n alpha\n-wrong\n+gamma\n*** End Patch\n",
		})
		if !res.IsError {
			t.Fatal("expected patch mismatch error")
		}
		mm, ok := errors.AsType[*tool.PatchMismatchError](res.Err)
		if !ok {
			t.Fatalf("err = %T (%v), want *tool.PatchMismatchError", res.Err, res.Err)
		}
		if mm.FileState != tool.PatchFileStateUnknown {
			t.Fatalf("FileState = %v, want PatchFileStateUnknown", mm.FileState)
		}
		if strings.Contains(res.Output, "the file has changed") || strings.Contains(res.Output, "the file has not changed") {
			t.Fatalf("expected neutral hint for unseen file, got:\n%s", res.Output)
		}
	})

	t.Run("move tracks destination and delete clears stamp", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		src := filepath.Join(dir, "src.txt")
		mtime := time.Date(2026, 8, 9, 10, 11, 12, 0, time.UTC)
		pinReadFile(t, src, "alpha\nbeta\n", mtime)
		s := newSession(t, withDir(dir))

		if res := execSessionTool(t, s, "r1", "read_file", map[string]any{"file_path": "src.txt"}); res.IsError {
			t.Fatalf("read_file: %s", res.Output)
		}
		if res := execSessionTool(t, s, "p-move", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: src.txt\n*** Move to: dst.txt\n@@\n alpha\n-beta\n+BETA\n*** End Patch\n",
		}); res.IsError {
			t.Fatalf("apply_patch move: %s", res.Output)
		}
		if s.readBeforeWriteWarning("dst.txt") != "" {
			t.Fatal("apply_patch Move did not track destination")
		}

		// Unchanged mismatch on dst.txt reports unchanged.
		missDst := execSessionTool(t, s, "p-miss-dst", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: dst.txt\n@@\n alpha\n-wrong\n+gamma\n*** End Patch\n",
		})
		if mmDst, ok := errors.AsType[*tool.PatchMismatchError](missDst.Err); !ok || mmDst.FileState != tool.PatchFileStateUnchanged {
			t.Fatalf("dst mismatch = %v (state=%v), want PatchFileStateUnchanged", missDst.Err, mmDst)
		}

		// Externally recreating src.txt with the original stamp must be Unknown (source stamp cleared).
		pinReadFile(t, src, "alpha\nbeta\n", mtime)
		missSrc := execSessionTool(t, s, "p-miss-src", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: src.txt\n@@\n alpha\n-wrong\n+gamma\n*** End Patch\n",
		})
		if mmSrc, ok := errors.AsType[*tool.PatchMismatchError](missSrc.Err); !ok || mmSrc.FileState != tool.PatchFileStateUnknown {
			t.Fatalf("recreated src mismatch = %v (state=%v), want PatchFileStateUnknown", missSrc.Err, mmSrc)
		}

		// Deleting dst.txt clears its stamp too.
		dst := filepath.Join(dir, "dst.txt")
		dstInfo, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}
		if res := execSessionTool(t, s, "p-del", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Delete File: dst.txt\n*** End Patch\n",
		}); res.IsError {
			t.Fatalf("apply_patch delete: %s", res.Output)
		}
		pinReadFile(t, dst, "alpha\nBETA\n", dstInfo.ModTime())
		missDeleted := execSessionTool(t, s, "p-miss-del", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: dst.txt\n@@\n alpha\n-wrong\n+gamma\n*** End Patch\n",
		})
		if mmDel, ok := errors.AsType[*tool.PatchMismatchError](missDeleted.Err); !ok || mmDel.FileState != tool.PatchFileStateUnknown {
			t.Fatalf("recreated deleted file mismatch = %v (state=%v), want PatchFileStateUnknown", missDeleted.Err, mmDel)
		}
	})

	t.Run("partial same-file patch mismatch reports changed and records landed write", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "f.txt")
		if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := newSession(t, withDir(dir))
		if res := execSessionTool(t, s, "r1", "read_file", map[string]any{"file_path": "f.txt"}); res.IsError {
			t.Fatalf("read_file: %s", res.Output)
		}
		// Section 1 rewrites "alpha" -> "ALPHA" (same byte length, often same mtime
		// tick on fast filesystems); section 2 mismatches.
		partial := strings.Join([]string{
			"*** Begin Patch",
			"*** Update File: f.txt",
			"@@",
			"-alpha",
			"+ALPHA",
			"*** Update File: f.txt",
			"@@",
			"-missing",
			"+nope",
			"*** End Patch",
			"",
		}, "\n")
		res := execSessionTool(t, s, "p-partial", "apply_patch", map[string]any{"patch": partial})
		if mm, ok := errors.AsType[*tool.PatchMismatchError](res.Err); !ok || mm.FileState != tool.PatchFileStateChanged {
			t.Fatalf("partial mismatch = %v (state=%v), want PatchFileStateChanged", res.Err, mm)
		}
		// The follow-up mismatch without any external edit now matches section 1's landed stamp.
		after := execSessionTool(t, s, "p-after", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: f.txt\n@@\n ALPHA\n-missing\n+nope\n*** End Patch\n",
		})
		if mmAfter, ok := errors.AsType[*tool.PatchMismatchError](after.Err); !ok || mmAfter.FileState != tool.PatchFileStateUnchanged {
			t.Fatalf("follow-up mismatch = %v (state=%v), want PatchFileStateUnchanged", after.Err, mmAfter)
		}
	})

	t.Run("failed move records committed source write and clears priorReads", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		blockedDir := filepath.Join(dir, "blocked_dir")
		if err := os.Mkdir(blockedDir, 0o755); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(dir, "src.txt")
		if err := os.WriteFile(src, []byte("alpha\nbeta\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s := newSession(t, withDir(dir))
		if res := execSessionTool(t, s, "r-src", "read_file", map[string]any{"file_path": "src.txt"}); res.IsError {
			t.Fatalf("read_file src.txt: %s", res.Output)
		}

		// Rewrite changes byte size and then fails RenamePath onto blocked_dir.
		moveFail := execSessionTool(t, s, "p-move-fail", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: src.txt\n*** Move to: blocked_dir\n@@\n alpha\n-beta\n+BETA-EXTENDED\n*** End Patch\n",
		})
		if !moveFail.IsError {
			t.Fatalf("expected rename failure onto existing directory, got: %q", moveFail.Output)
		}
		// Destination directory must not be tracked as read or stamped.
		blockedKey := s.resolveFilePath(blockedDir)
		s.readFilesMu.RLock()
		_, dstRead := s.readFiles[blockedKey]
		_, dstStamped := s.fileStamps[blockedKey]
		s.readFilesMu.RUnlock()
		if dstRead || dstStamped {
			t.Fatalf("failed move tracked destination directory: read=%v stamped=%v", dstRead, dstStamped)
		}
		// Retrying apply_patch against src.txt expecting pre-edit text must see
		// the refreshed post-write stamp (Unchanged, not Changed).
		retry := execSessionTool(t, s, "p-retry", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: src.txt\n@@\n alpha\n-beta\n+gamma\n*** End Patch\n",
		})
		if mm, ok := errors.AsType[*tool.PatchMismatchError](retry.Err); !ok || mm.FileState != tool.PatchFileStateUnchanged {
			t.Fatalf("retry mismatch = %v (state=%v), want PatchFileStateUnchanged", retry.Err, mm)
		}

		// Same-size failed move with pinned mtime must still invalidate priorReads.
		mtime := time.Date(2026, 9, 10, 11, 12, 13, 0, time.UTC)
		var b strings.Builder
		for i := 1; i <= 20; i++ {
			fmt.Fprintf(&b, "line-%02d\n", i)
		}
		same := filepath.Join(dir, "same.txt")
		pinReadFile(t, same, b.String(), mtime)
		if first := repeatReadFile(t, s, "r-same-1", same, map[string]any{"offset": 1, "limit": 20}); first.UnchangedSinceTurn != nil {
			t.Fatal("first read must not be marked unchanged")
		}
		if res := execSessionTool(t, s, "p-same-move-fail", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: same.txt\n*** Move to: blocked_dir\n@@\n line-06\n-line-07\n+LINE-07\n line-08\n*** End Patch\n",
		}); !res.IsError {
			t.Fatalf("expected rename failure on same.txt, got: %q", res.Output)
		}
		if err := os.Chtimes(same, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		after := repeatReadFile(t, s, "r-same-2", same, map[string]any{"offset": 5, "limit": 6})
		if after.UnchangedSinceTurn != nil {
			t.Fatalf("failed move did not invalidate priorReads: subset read falsely marked unchanged since turn %d", *after.UnchangedSinceTurn)
		}
		if !strings.Contains(after.Output, "LINE-07") {
			t.Fatalf("subset read after failed move missing committed edit: %q", after.Output)
		}
	})

	t.Run("symlinked parent paths share tracking for write, edit, mismatch, and delete", func(t *testing.T) {
		t.Parallel()
		base := t.TempDir()
		realDir := filepath.Join(base, "real")
		if err := os.MkdirAll(realDir, 0o755); err != nil {
			t.Fatal(err)
		}
		linkDir := filepath.Join(base, "link")
		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Fatal(err)
		}
		linkFile := filepath.Join(linkDir, "file.txt")
		realFile := filepath.Join(realDir, "file.txt")

		s := newSession(t, withDir(base))

		// Create via symlinked parent.
		if res := execSessionTool(t, s, "w-link", "write_file", map[string]any{
			"file_path": linkFile,
			"content":   "alpha\nbeta\n",
		}); res.IsError {
			t.Fatalf("write_file via link: %s", res.Output)
		}

		// Patch mismatch via canonical parent must recognize the unchanged write.
		missReal := execSessionTool(t, s, "p-miss-real", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: " + realFile + "\n@@\n alpha\n-wrong\n+gamma\n*** End Patch\n",
		})
		if mm, ok := errors.AsType[*tool.PatchMismatchError](missReal.Err); !ok || mm.FileState != tool.PatchFileStateUnchanged {
			t.Fatalf("mismatch via real path = %v, want PatchFileStateUnchanged", missReal.Err)
		}

		// Edit via canonical parent must not emit an unread-file warning.
		ed := execSessionTool(t, s, "e-real", "edit_file", map[string]any{
			"file_path": realFile,
			"edits":     []any{map[string]any{"old_string": "beta", "new_string": "BETA-UPDATED"}},
		})
		if ed.IsError {
			t.Fatalf("edit_file via real path: %s", ed.Output)
		}
		if strings.Contains(ed.Output, "WARNING:") {
			t.Fatalf("unexpected unread warning editing via canonical parent after symlink write: %q", ed.Output)
		}

		// Patch mismatch via symlinked parent must see the refreshed stamp from the real-path edit.
		missLink := execSessionTool(t, s, "p-miss-link", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: " + linkFile + "\n@@\n alpha\n-wrong\n+gamma\n*** End Patch\n",
		})
		if mm, ok := errors.AsType[*tool.PatchMismatchError](missLink.Err); !ok || mm.FileState != tool.PatchFileStateUnchanged {
			t.Fatalf("mismatch via link path after real edit = %v, want PatchFileStateUnchanged", missLink.Err)
		}

		// Delete via symlinked parent must clear the shared stamp for the canonical parent too.
		info, err := os.Stat(realFile)
		if err != nil {
			t.Fatal(err)
		}
		if res := execSessionTool(t, s, "p-del-link", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Delete File: " + linkFile + "\n*** End Patch\n",
		}); res.IsError {
			t.Fatalf("delete via link path: %s", res.Output)
		}
		pinReadFile(t, realFile, "alpha\nBETA-UPDATED\n", info.ModTime())
		missAfterDel := execSessionTool(t, s, "p-miss-after-del", "apply_patch", map[string]any{
			"patch": "*** Begin Patch\n*** Update File: " + realFile + "\n@@\n alpha\n-wrong\n+gamma\n*** End Patch\n",
		})
		if mm, ok := errors.AsType[*tool.PatchMismatchError](missAfterDel.Err); !ok || mm.FileState != tool.PatchFileStateUnknown {
			t.Fatalf("mismatch via real path after link delete = %v, want PatchFileStateUnknown", missAfterDel.Err)
		}
	})
}

func TestMemoryWriteAndEditTrackReadState(t *testing.T) {
	t.Parallel()

	t.Run("memory_write suppresses warning on subsequent memory_edit with scope isolation", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		s := newSession(t, withConfig(SessionConfig{
			StateDir:        t.TempDir(),
			MemoryStateRoot: root,
			MemoryProjectID: "proj-iso",
		}))

		// Pre-create an unread page with the same relative name in project scope.
		projEnv, err := s.openMemoryEnvironment("project")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := projEnv.WriteFile("shared.md", "---\nwhen: project fact\n---\nold-project\n"); err != nil {
			t.Fatal(err)
		}

		// Write shared.md in personal scope, then edit it in personal scope: no warning.
		if res := memoryExec(t, s, "memory_write", map[string]any{
			"scope":     "personal",
			"file_path": "shared.md",
			"when":      "personal fact",
			"body":      "old-personal\n",
		}); res.IsError || strings.Contains(res.Output, "WARNING:") {
			t.Fatalf("personal memory_write = %+v", res)
		}
		if res := memoryExec(t, s, "memory_edit", map[string]any{
			"scope":     "personal",
			"file_path": "shared.md",
			"edits":     []any{map[string]any{"old_string": "old-personal", "new_string": "new-personal"}},
		}); res.IsError || strings.Contains(res.Output, "WARNING:") {
			t.Fatalf("personal memory_edit after memory_write = %+v", res)
		}

		// Project scope must remain isolated: first memory_edit warns, second does not.
		firstProj := memoryExec(t, s, "memory_edit", map[string]any{
			"scope":     "project",
			"file_path": "shared.md",
			"edits":     []any{map[string]any{"old_string": "old-project", "new_string": "mid-project"}},
		})
		if firstProj.IsError || !strings.Contains(firstProj.Output, "WARNING:") {
			t.Fatalf("first project memory_edit expected unread warning, got %+v", firstProj)
		}
		secondProj := memoryExec(t, s, "memory_edit", map[string]any{
			"scope":     "project",
			"file_path": "shared.md",
			"edits":     []any{map[string]any{"old_string": "mid-project", "new_string": "new-project"}},
		})
		if secondProj.IsError || strings.Contains(secondProj.Output, "WARNING:") {
			t.Fatalf("second project memory_edit unexpected warning: %+v", secondProj)
		}
	})

	t.Run("failed memory write and edit do not track path and delete clears stamp", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		s := newSession(t, withConfig(SessionConfig{
			StateDir:        t.TempDir(),
			MemoryStateRoot: root,
			MemoryProjectID: "proj-fail",
		}))
		env, err := s.openMemoryEnvironment("personal")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.WriteFile("unread.md", "---\nwhen: existing\n---\nbody\n"); err != nil {
			t.Fatal(err)
		}
		abs := filepath.Join(env.WorkingDirectory(), "unread.md")

		// Failed memory_write (missing when) must not track.
		if res := memoryExec(t, s, "memory_write", map[string]any{
			"scope":     "personal",
			"file_path": "unread.md",
			"when":      "",
			"body":      "new\n",
		}); !res.IsError {
			t.Fatalf("expected memory_write without when to fail: %+v", res)
		}
		if s.fileReadGuard(env).ReadBeforeWriteWarning(abs) == "" {
			t.Fatal("failed memory_write tracked unread page")
		}

		// Failed memory_edit (non-matching old_string) must not track.
		if res := memoryExec(t, s, "memory_edit", map[string]any{
			"scope":     "personal",
			"file_path": "unread.md",
			"edits":     []any{map[string]any{"old_string": "absent", "new_string": "x"}},
		}); !res.IsError {
			t.Fatalf("expected memory_edit mismatch to fail: %+v", res)
		}
		if s.fileReadGuard(env).ReadBeforeWriteWarning(abs) == "" {
			t.Fatal("failed memory_edit tracked unread page")
		}

		// Successful memory_edit tracks the stamp, and memory_delete removes it.
		if res := memoryExec(t, s, "memory_edit", map[string]any{
			"scope":     "personal",
			"file_path": "unread.md",
			"edits":     []any{map[string]any{"old_string": "body", "new_string": "updated"}},
		}); res.IsError {
			t.Fatalf("memory_edit failed: %s", res.Output)
		}
		key := s.resolveFilePath(abs)
		s.readFilesMu.RLock()
		_, hasStampBeforeDelete := s.fileStamps[key]
		s.readFilesMu.RUnlock()
		if !hasStampBeforeDelete {
			t.Fatal("successful memory_edit did not record file stamp")
		}

		if res := memoryExec(t, s, "memory_delete", map[string]any{
			"scope":     "personal",
			"file_path": "unread.md",
		}); res.IsError {
			t.Fatalf("memory_delete failed: %s", res.Output)
		}
		s.readFilesMu.RLock()
		_, hasStampAfterDelete := s.fileStamps[key]
		s.readFilesMu.RUnlock()
		if hasStampAfterDelete {
			t.Fatal("memory_delete did not remove recorded file stamp")
		}
	})
}
