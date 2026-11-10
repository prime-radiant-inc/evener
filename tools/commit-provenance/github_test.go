package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func githubAPI(t *testing.T, method, path string, body any, result any) {
	t.Helper()
	args := []string{"api", "--hostname", "github.com", "--method", method, path}
	var input []byte
	if body != nil {
		var err error
		input, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		args = append(args, "--input", "-")
	}
	cmd := exec.Command("gh", args...)
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("GitHub %s %s: %v\n%s", method, path, err, &stderr)
	}
	if err := json.Unmarshal(output, result); err != nil {
		t.Fatal(err)
	}
}

// This live experiment writes synthetic branches and merges test pull requests.
// It leaves them on GitHub for inspection and never touches an existing branch.
func TestGitHubProvenance(t *testing.T) {
	if os.Getenv("EVENER_PROVENANCE_GITHUB_E2E") != "1" {
		t.Skip("live GitHub experiment requires explicit opt-in")
	}
	destination := os.Getenv("EVENER_PROVENANCE_GITHUB_REPO")
	parts := strings.Split(destination, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		t.Fatal("set EVENER_PROVENANCE_GITHUB_REPO to an authorized disposable owner/repo")
	}
	if config := os.Getenv("GH_CONFIG_DIR"); config == "" || !filepath.IsAbs(config) {
		t.Fatal("set GH_CONFIG_DIR to the absolute gh configuration directory before test HOME isolation")
	}
	api := "repos/" + destination
	var remote struct {
		Private  bool   `json:"private"`
		FullName string `json:"full_name"`
	}
	githubAPI(t, "GET", api, nil, &remote)
	if !remote.Private || !strings.EqualFold(remote.FullName, destination) {
		t.Fatal("live experiment requires the explicitly named private repository")
	}

	repo := testRepo(t)
	base := testCommit(t, repo, "base", "synthetic provenance experiment\n")
	testCommit(t, repo, "change", "synthetic source change\n")
	source := attachRecord(t, repo)
	raw := testGit(t, repo, nil, "cat-file", "commit", source)
	prefix := "probe-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	url := "https://github.com/" + destination + ".git"
	credential := "credential.helper=!gh auth git-credential"
	push := []string{"-c", credential, "push", url}
	for _, suffix := range []string{"base", "merge-base", "squash-base", "rebase-base"} {
		push = append(push, base+":refs/heads/"+prefix+"-"+suffix)
	}
	for _, suffix := range []string{"source", "merge-source", "squash-source", "rebase-source"} {
		push = append(push, source+":refs/heads/"+prefix+"-"+suffix)
	}
	// Empty expected values require every destination ref to be absent.
	for _, suffix := range []string{"base", "merge-base", "squash-base", "rebase-base", "source", "merge-source", "squash-source", "rebase-source"} {
		push = append(push, "--force-with-lease=refs/heads/"+prefix+"-"+suffix+":")
	}
	testGit(t, repo, nil, push...)
	clone := filepath.Join(t.TempDir(), "clone")
	testGit(t, repo, nil, "-c", credential, "clone", "--branch", prefix+"-source", url, clone)
	head := strings.TrimSpace(string(testGit(t, clone, nil, "rev-parse", "HEAD")))
	clonedRaw := testGit(t, clone, nil, "cat-file", "commit", "HEAD")
	if head != source || !bytes.Equal(raw, clonedRaw) {
		t.Fatal("fresh GitHub clone changed the source object")
	}
	testGit(t, clone, nil, "fsck", "--strict")
	t.Logf("GitHub transport: repository=%s branch=%s-source oid=%s exact_bytes=true", destination, prefix, source)

	var apiCommit map[string]json.RawMessage
	githubAPI(t, "GET", api+"/git/commits/"+source, nil, &apiCommit)
	_, exposed := apiCommit["evener-provenance"]
	t.Logf("GitHub commit API: custom_header_top_level_field=%t", exposed)
	for _, method := range []string{"merge", "squash", "rebase"} {
		t.Run(method, func(t *testing.T) {
			var pr struct {
				Number int    `json:"number"`
				URL    string `json:"html_url"`
			}
			githubAPI(t, "POST", api+"/pulls", map[string]string{
				"title": "Synthetic commit provenance: " + method,
				"body":  "Disposable fixture. No Evener source or real session metadata. Tests custom commit-header preservation.",
				"head":  prefix + "-" + method + "-source",
				"base":  prefix + "-" + method + "-base",
			}, &pr)
			if pr.Number == 0 {
				t.Fatal("GitHub did not return a pull request number")
			}
			var merged struct {
				Merged bool   `json:"merged"`
				SHA    string `json:"sha"`
			}
			githubAPI(t, "PUT", fmt.Sprintf("%s/pulls/%d/merge", api, pr.Number), map[string]string{
				"merge_method": method, "sha": source,
			}, &merged)
			if !merged.Merged || merged.SHA == "" {
				t.Fatalf("GitHub did not merge fixture PR %d", pr.Number)
			}
			branch := prefix + "-" + method + "-base"
			testGit(t, clone, nil, "-c", credential, "fetch", "origin", "refs/heads/"+branch)
			tip := strings.TrimSpace(string(testGit(t, clone, nil, "rev-parse", "FETCH_HEAD")))
			if tip != merged.SHA || tip == source {
				t.Fatal("server merge did not produce the reported new commit")
			}
			if body := testGit(t, clone, nil, "show", tip+":change"); !bytes.Equal(body, []byte("synthetic source change\n")) {
				t.Fatal("server merge did not carry the source change")
			}
			records := readRecords(t, testGit(t, clone, nil, "cat-file", "commit", tip))
			reachable := false
			for ancestor := range strings.FieldsSeq(string(testGit(t, clone, nil, "rev-list", tip))) {
				reachable = reachable || ancestor == source
			}
			if reachable != (method == "merge") {
				t.Fatal("server merge produced unexpected source ancestry")
			}
			t.Logf("GitHub %s: pr=%s result=%s tip_records=%d original_reachable=%t", method, pr.URL, tip, len(records), reachable)
		})
	}
}
