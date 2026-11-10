package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testGit(t *testing.T, repo string, input []byte, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func testRepo(t *testing.T) string {
	t.Helper()
	// Not parallel: isolate Git configuration and identity for every subprocess.
	home := t.TempDir()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "Provenance Fixture")
	t.Setenv("GIT_AUTHOR_EMAIL", "fixture@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Provenance Fixture")
	t.Setenv("GIT_COMMITTER_EMAIL", "fixture@example.invalid")
	t.Setenv("GIT_AUTHOR_DATE", "2001-01-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2001-01-01T00:00:00Z")
	repo := t.TempDir()
	testGit(t, repo, nil, "init", "-b", "main")
	return repo
}

func testCommit(t *testing.T, repo, path, contents string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, path), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, nil, "add", "--", path)
	testGit(t, repo, nil, "commit", "-m", "fixture")
	return strings.TrimSpace(string(testGit(t, repo, nil, "rev-parse", "HEAD")))
}

func testRecord() provenance {
	return provenance{Schema: 1, EvenerVersion: "fixture-version", Model: "fixture-provider/fixture-model", Host: "fixture-host", SessionID: "fixture-session"}
}

func readRecords(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	headers, _, ok := bytes.Cut(raw, []byte("\n\n"))
	if !ok {
		t.Fatal("commit has no header separator")
	}
	var records []map[string]any
	for line := range bytes.SplitSeq(headers, []byte("\n")) {
		if data, found := bytes.CutPrefix(line, []byte("evener-provenance ")); found {
			var record map[string]any
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			records = append(records, record)
		}
	}
	return records
}

func TestStampPreservesCommitAndWorkspace(t *testing.T) {
	repo := testRepo(t)
	original := testCommit(t, repo, "tracked", "committed bytes\n")
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("uncommitted bytes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := testGit(t, repo, nil, "cat-file", "commit", original)
	status := testGit(t, repo, nil, "status", "--porcelain=v1")
	index := testGit(t, repo, nil, "ls-files", "--stage")
	stamped, err := stamp(repo, original, testRecord())
	if err != nil {
		t.Fatal(err)
	}
	after := testGit(t, repo, nil, "cat-file", "commit", stamped)
	records := readRecords(t, after)
	if len(records) != 1 {
		t.Fatalf("got %d provenance records, want 1", len(records))
	}
	want := map[string]any{"schema": float64(1), "evener_version": "fixture-version", "model": "fixture-provider/fixture-model", "host": "fixture-host", "session_id": "fixture-session"}
	for key, value := range want {
		if records[0][key] != value {
			t.Errorf("field %s = %v, want %v", key, records[0][key], value)
		}
	}
	oldHeaders, oldMessage, _ := bytes.Cut(before, []byte("\n\n"))
	newHeaders, newMessage, _ := bytes.Cut(after, []byte("\n\n"))
	if !bytes.HasPrefix(newHeaders, append(oldHeaders, '\n')) || !bytes.Equal(oldMessage, newMessage) {
		t.Fatal("original commit headers or message changed")
	}
	if got := strings.TrimSpace(string(testGit(t, repo, nil, "rev-parse", "HEAD"))); got != original {
		t.Fatal("stamp moved HEAD")
	}
	if !bytes.Equal(status, testGit(t, repo, nil, "status", "--porcelain=v1")) || !bytes.Equal(index, testGit(t, repo, nil, "ls-files", "--stage")) {
		t.Fatal("stamp changed workspace or index")
	}
	if !bytes.Equal(before, testGit(t, repo, nil, "cat-file", "commit", original)) {
		t.Fatal("original object changed")
	}
	testGit(t, repo, nil, "fsck", "--strict", "--no-reflogs", stamped)
}

func TestStampDeduplicatesRecordAndKeepsOtherSessions(t *testing.T) {
	repo := testRepo(t)
	original := testCommit(t, repo, "tracked", "data\n")
	first, err := stamp(repo, original, testRecord())
	if err != nil {
		t.Fatal(err)
	}
	again, err := stamp(repo, first, testRecord())
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Error("repeating the same record changed the commit")
	}
	other := testRecord()
	other.SessionID = "another-fixture-session"
	second, err := stamp(repo, first, other)
	if err != nil {
		t.Fatal(err)
	}
	records := readRecords(t, testGit(t, repo, nil, "cat-file", "commit", second))
	if len(records) != 2 || records[0]["session_id"] != "fixture-session" || records[1]["session_id"] != "another-fixture-session" {
		t.Fatalf("contributor records = %v", records)
	}
}

func TestStampRejectsIncompleteOrMultilineRecords(t *testing.T) {
	repo := testRepo(t)
	original := testCommit(t, repo, "tracked", "data\n")
	cases := map[string]provenance{}
	for _, field := range []string{"schema", "version", "model", "host", "session"} {
		record := testRecord()
		switch field {
		case "schema":
			record.Schema = 0
		case "version":
			record.EvenerVersion = ""
		case "model":
			record.Model = ""
		case "host":
			record.Host = ""
		case "session":
			record.SessionID = ""
		}
		cases[field] = record
	}
	for _, value := range []string{"\nparent forged", "\r", "\x00", "   ", "\xff"} {
		record := testRecord()
		record.SessionID = value
		cases[value] = record
	}
	for name, record := range cases {
		t.Run(name, func(t *testing.T) {
			if oid, err := stamp(repo, original, record); !errors.Is(err, errInvalidRecord) || oid != "" {
				t.Fatalf("invalid record produced oid=%q err=%v", oid, err)
			}
		})
	}
}

func TestStampRefusesToInvalidateRealSignature(t *testing.T) {
	repo := testRepo(t)
	key := filepath.Join(t.TempDir(), "signing-key")
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(t.TempDir(), "allowed-signers")
	if err := os.WriteFile(allowed, append([]byte("fixture@example.invalid "), pub...), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, nil, "config", "gpg.format", "ssh")
	testGit(t, repo, nil, "config", "user.signingkey", key)
	testGit(t, repo, nil, "config", "gpg.ssh.allowedSignersFile", allowed)
	testGit(t, repo, nil, "config", "commit.gpgsign", "true")
	original := testCommit(t, repo, "tracked", "signed data\n")
	testGit(t, repo, nil, "verify-commit", original)
	if oid, err := stamp(repo, original, testRecord()); !errors.Is(err, errSignedCommit) || oid != "" {
		t.Fatalf("signed commit produced oid=%q err=%v", oid, err)
	}
	testGit(t, repo, nil, "verify-commit", original)
	if got := strings.TrimSpace(string(testGit(t, repo, nil, "rev-parse", "HEAD"))); got != original {
		t.Fatal("signed commit was moved")
	}
	// An independent mutation proves why inserting a header after signing is unsafe.
	raw := testGit(t, repo, nil, "cat-file", "commit", original)
	mutated := bytes.Replace(raw, []byte("\n\n"), []byte("\nevener-provenance {}\n\n"), 1)
	badOID := strings.TrimSpace(string(testGit(t, repo, mutated, "hash-object", "-t", "commit", "-w", "--stdin")))
	cmd = exec.Command("git", "-C", repo, "verify-commit", badOID)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("modified signed object still verified: %s", output)
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("verification did not run to a signature rejection: %v\n%s", err, output)
		}
	}
}

func TestCommandWritesObjectWithoutMovingBranch(t *testing.T) {
	repo := testRepo(t)
	original := testCommit(t, repo, "tracked", "data\n")
	args := []string{"-repo", repo, "-commit", original, "-evener-version", "fixture-version", "-model", "fixture-provider/fixture-model", "-host", "fixture-host", "-session", "fixture-session"}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("command exit=%d stderr=%s", code, &stderr)
	}
	oid := strings.TrimSpace(stdout.String())
	if oid == "" || oid == original {
		t.Fatalf("command did not return a new commit: %q", oid)
	}
	if got := len(readRecords(t, testGit(t, repo, nil, "cat-file", "commit", oid))); got != 1 {
		t.Fatalf("command wrote %d records", got)
	}
	if len(stderr.Bytes()) != 0 {
		t.Fatalf("successful command emitted stderr: %s", &stderr)
	}
	invalidRevision := append([]string(nil), args...)
	invalidRevision[3] = "--not-a-revision"
	stdout.Reset()
	stderr.Reset()
	if code := run(invalidRevision, &stdout, &stderr); code == 0 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("invalid revision exit=%d stdout=%q stderr=%q", code, &stdout, &stderr)
	}
	for _, badArgs := range [][]string{nil, {"-unknown"}, {"-h"}} {
		stdout.Reset()
		stderr.Reset()
		code := run(badArgs, &stdout, &stderr)
		if len(badArgs) == 1 && badArgs[0] == "-h" {
			if code != 0 || stderr.Len() == 0 {
				t.Fatalf("help exit=%d bytes=%d", code, stderr.Len())
			}
		} else if code == 0 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("invalid invocation exit=%d stdout=%q stderr=%q", code, &stdout, &stderr)
		}
	}
	if got := strings.TrimSpace(string(testGit(t, repo, nil, "rev-parse", "HEAD"))); got != original {
		t.Fatal("command moved the branch")
	}
}

func TestStampPreservesUnknownHeadersAndRawMessage(t *testing.T) {
	repo := testRepo(t)
	original := testCommit(t, repo, "tracked", "data\n")
	raw := testGit(t, repo, nil, "cat-file", "commit", original)
	headers, _, _ := bytes.Cut(raw, []byte("\n\n"))
	headers = append(headers, []byte("\nx-fixture first\n continuation\nencoding ISO-8859-1")...)
	message := []byte{'m', 0xe9, '\n', '\n', 'x', '\n'}
	fixture := append(append(append([]byte(nil), headers...), '\n', '\n'), message...)
	oid := strings.TrimSpace(string(testGit(t, repo, fixture, "hash-object", "-t", "commit", "-w", "--stdin")))
	stamped, err := stamp(repo, oid, testRecord())
	if err != nil {
		t.Fatal(err)
	}
	got := testGit(t, repo, nil, "cat-file", "commit", stamped)
	newHeaders, newMessage, _ := bytes.Cut(got, []byte("\n\n"))
	if !bytes.HasPrefix(newHeaders, append(headers, '\n')) || !bytes.Equal(newMessage, message) {
		t.Fatal("unknown headers or raw message bytes were changed")
	}
}

func TestStampSupportsSHA256Objects(t *testing.T) {
	testRepo(t)
	repo := t.TempDir()
	testGit(t, repo, nil, "init", "--object-format=sha256", "-b", "main")
	original := testCommit(t, repo, "tracked", "data\n")
	stamped, err := stamp(repo, original, testRecord())
	if err != nil {
		t.Fatal(err)
	}
	if len(stamped) != 64 || stamped == original {
		t.Fatalf("unexpected SHA-256 object ID %q", stamped)
	}
	if got := len(readRecords(t, testGit(t, repo, nil, "cat-file", "commit", stamped))); got != 1 {
		t.Fatalf("SHA-256 object records = %d", got)
	}
	testGit(t, repo, nil, "fsck", "--strict", "--no-reflogs", stamped)
}

func TestStampUsesExplicitRepositoryDespiteInheritedRouting(t *testing.T) {
	for _, variable := range []string{"GIT_DIR", "GIT_OBJECT_DIRECTORY"} {
		t.Run(variable, func(t *testing.T) {
			repo := testRepo(t)
			original := testCommit(t, repo, "tracked", "requested repository\n")
			other := testRepo(t)
			testCommit(t, other, "tracked", "unrelated repository\n")
			inventory := testGit(t, other, nil, "cat-file", "--batch-all-objects", "--batch-check")
			var stamped string
			t.Run("inherited-environment", func(t *testing.T) {
				path := filepath.Join(other, ".git")
				if variable == "GIT_OBJECT_DIRECTORY" {
					path = filepath.Join(path, "objects")
				}
				t.Setenv(variable, path)
				var err error
				stamped, err = stamp(repo, "HEAD", testRecord())
				if err != nil {
					t.Fatalf("explicit repository could not be stamped: %v", err)
				}
			})
			if !bytes.Equal(inventory, testGit(t, other, nil, "cat-file", "--batch-all-objects", "--batch-check")) {
				t.Error("stamp wrote an object in the unrelated repository")
			}
			if stamped == "" {
				return
			}
			if body := testGit(t, repo, nil, "show", stamped+":tracked"); !bytes.Equal(body, []byte("requested repository\n")) {
				t.Fatal("stamp used the unrelated repository's tree")
			}
			if got := strings.TrimSpace(string(testGit(t, repo, nil, "rev-parse", "HEAD"))); got != original {
				t.Fatal("stamp moved the requested repository's branch")
			}
		})
	}
}
