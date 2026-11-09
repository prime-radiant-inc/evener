package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func attachRecord(t *testing.T, repo string) string {
	t.Helper()
	before := strings.TrimSpace(string(testGit(t, repo, nil, "rev-parse", "HEAD")))
	after, err := stamp(repo, before, testRecord())
	if err != nil {
		t.Fatal(err)
	}
	// Only disposable fixture refs are moved. The prototype itself never moves refs.
	testGit(t, repo, nil, "update-ref", "HEAD", after, before)
	return after
}

func TestGitTransportKeepsExactCommitObject(t *testing.T) {
	repo := testRepo(t)
	testCommit(t, repo, "tracked", "fixture\n")
	oid := attachRecord(t, repo)
	raw := testGit(t, repo, nil, "cat-file", "commit", oid)
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	testGit(t, root, nil, "init", "--bare", "-b", "main", remote)
	testGit(t, remote, nil, "config", "receive.fsckObjects", "true")
	testGit(t, repo, nil, "push", remote, "HEAD:refs/heads/main")
	clone := filepath.Join(root, "clone")
	testGit(t, root, nil, "clone", "--no-local", "file://"+remote, clone)
	if got := strings.TrimSpace(string(testGit(t, clone, nil, "rev-parse", "HEAD"))); got != oid {
		t.Fatalf("clone HEAD = %s, want %s", got, oid)
	}
	clonedRaw := testGit(t, clone, nil, "cat-file", "commit", "HEAD")
	if !bytes.Equal(raw, clonedRaw) {
		t.Fatal("wire transport changed commit bytes")
	}
	if got := len(readRecords(t, clonedRaw)); got != 1 {
		t.Fatalf("clone records = %d", got)
	}
	testGit(t, clone, nil, "fsck", "--strict")
	if refs := testGit(t, clone, nil, "for-each-ref", "--format=%(refname)", "refs/notes"); len(refs) != 0 {
		t.Fatalf("unexpected notes dependency: %s", refs)
	}
	// The standard message-only view stays byte-identical.
	if !bytes.Equal(testGit(t, repo, nil, "show", "-s", "--format=%B", "HEAD"), testGit(t, clone, nil, "show", "-s", "--format=%B", "HEAD")) {
		t.Fatal("transport changed the message")
	}
}

func TestGitRewriteHeaderBehavior(t *testing.T) {
	for _, tc := range []struct {
		operation   string
		wantRecords int
		keepsSource bool
	}{
		{"amend", 1, false},
		{"rebase", 0, false},
		{"cherry-pick", 0, false},
		{"squash", 0, false},
		{"merge", 0, true},
		{"fast-forward", 1, true},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			repo := testRepo(t)
			testCommit(t, repo, "base", "base\n")
			testGit(t, repo, nil, "switch", "-c", "topic")
			testCommit(t, repo, "change", "topic data\n")
			source := attachRecord(t, repo)
			if tc.operation == "amend" {
				testGit(t, repo, nil, "commit", "--amend", "-m", "amended fixture")
			} else {
				testGit(t, repo, nil, "switch", "main")
				if tc.operation != "fast-forward" {
					testCommit(t, repo, "other", "unrelated\n")
				}
				switch tc.operation {
				case "rebase":
					testGit(t, repo, nil, "switch", "topic")
					testGit(t, repo, nil, "rebase", "main")
				case "cherry-pick":
					testGit(t, repo, nil, "cherry-pick", "topic")
				case "squash":
					testGit(t, repo, nil, "merge", "--squash", "topic")
					testGit(t, repo, nil, "commit", "-m", "squash fixture")
				case "merge":
					testGit(t, repo, nil, "merge", "--no-ff", "--no-edit", "topic")
				case "fast-forward":
					testGit(t, repo, nil, "merge", "--ff-only", "topic")
				}
			}
			got := len(readRecords(t, testGit(t, repo, nil, "cat-file", "commit", "HEAD")))
			if got != tc.wantRecords {
				t.Fatalf("%s tip records = %d, want %d", tc.operation, got, tc.wantRecords)
			}
			head := strings.TrimSpace(string(testGit(t, repo, nil, "rev-parse", "HEAD")))
			if (head == source) != (tc.operation == "fast-forward") {
				t.Fatalf("%s did not produce the expected commit identity change", tc.operation)
			}
			if body := testGit(t, repo, nil, "show", "HEAD:change"); !bytes.Equal(body, []byte("topic data\n")) {
				t.Fatal("operation did not carry the source change")
			}
			ancestry := strings.Fields(string(testGit(t, repo, nil, "rev-list", "HEAD")))
			found := false
			for _, id := range ancestry {
				found = found || id == source
			}
			if found != tc.keepsSource {
				t.Fatalf("%s source reachability = %t, want %t", tc.operation, found, tc.keepsSource)
			}
			t.Logf("%s: tip_records=%d original_reachable=%t", tc.operation, got, found)
		})
	}
}

func TestStampDoesNotReplaceOrRerunCommitHooks(t *testing.T) {
	repo := testRepo(t)
	hooks := filepath.Join(repo, "custom-hooks")
	if err := os.Mkdir(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-commit", "commit-msg", "post-commit"} {
		body := "#!/bin/sh\nprintf '%s\\n' " + name + " >> hooks-called\n"
		if err := os.WriteFile(filepath.Join(hooks, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, repo, nil, "config", "core.hooksPath", hooks)
	original := testCommit(t, repo, "tracked", "data\n")
	before, err := os.ReadFile(filepath.Join(repo, "hooks-called"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, []byte("pre-commit\ncommit-msg\npost-commit\n")) {
		t.Fatalf("fixture hooks did not run: %s", before)
	}
	if _, err := stamp(repo, original, testRecord()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(repo, "hooks-called"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("stamp changed hook history: %v", err)
	}
	if got := strings.TrimSpace(string(testGit(t, repo, nil, "config", "core.hooksPath"))); got != hooks {
		t.Fatal("stamp changed hooks configuration")
	}
}

func TestPostCommitRewriteLeavesGitReportingOldObject(t *testing.T) {
	repo := testRepo(t)
	hook := `#!/bin/sh
set -eu
old=$(git rev-parse HEAD)
new=$(git cat-file commit "$old" | awk 'BEGIN { inserted=0 } /^$/ && !inserted { print "evener-provenance {}"; inserted=1 } { print }' | git hash-object -t commit -w --stdin)
git update-ref HEAD "$new" "$old"
printf '%s\n%s\n' "$old" "$new" > rewrite-receipt
`
	if err := os.WriteFile(filepath.Join(repo, ".git", "hooks", "post-commit"), []byte(hook), 0700); err != nil {
		t.Fatal(err)
	}
	output := testGit(t, repo, nil, "commit", "--allow-empty", "-m", "fixture")
	receipt, err := os.ReadFile(filepath.Join(repo, "rewrite-receipt"))
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Fields(string(receipt))
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("hook did not rewrite the fixture: %q", receipt)
	}
	abbrev := strings.TrimSpace(string(testGit(t, repo, nil, "rev-parse", "--short", ids[0])))
	if !bytes.Contains(output, []byte(abbrev)) {
		t.Fatalf("Git no longer reports original id %s: %s", abbrev, output)
	}
	if head := strings.TrimSpace(string(testGit(t, repo, nil, "rev-parse", "HEAD"))); head != ids[1] {
		t.Fatal("hook did not move HEAD")
	}
	t.Logf("post-commit rewrite: reported=%s head=%s", ids[0], ids[1])
}
