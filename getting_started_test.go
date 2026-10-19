package evener_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Execute the guide's actual setup block. Only the network clone source is
// redirected to a local repository; mkdir, cp, git and Python run for real.
func TestGettingStartedExercisePreservesExistingWork(t *testing.T) {
	doc, err := os.ReadFile("docs/getting-started.md")
	if err != nil {
		t.Fatal(err)
	}
	var script string
	for _, block := range regexp.MustCompile("(?s)```sh\\n(.*?)\\n```").FindAllSubmatch(doc, -1) {
		if bytes.Contains(block[1], []byte("evener-first-task")) {
			script = string(block[1])
			break
		}
	}
	if script == "" {
		t.Fatal("exercise setup block not found")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	examples := filepath.Join(source, "docs", "examples", "first-task")
	if err := os.MkdirAll(examples, 0700); err != nil {
		t.Fatal(err)
	}
	files := []string{"README.md", "receipt.py", "check.py"}
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join("docs", "examples", "first-task", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(examples, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = source
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init")
	runGit("add", "docs/examples/first-task")
	runGit("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	wrapper := `#!/bin/sh
if [ "$1" = clone ]; then
  for target; do :; done
  printf 'clone\n' >> "$TUTORIAL_CLONE_LOG"
  exec "$TUTORIAL_GIT" clone --no-hardlinks "$TUTORIAL_SOURCE" "$target"
fi
exec "$TUTORIAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	snapshot := func(dir string) map[string]string {
		t.Helper()
		out := map[string]string{}
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			out[rel] = string(data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, existing := range []string{"evener-docs", "evener-first-task", "both", "fresh"} {
		t.Run(existing, func(t *testing.T) {
			parent := t.TempDir()
			before := map[string]map[string]string{}
			for _, name := range []string{"evener-docs", "evener-first-task"} {
				if existing != name && existing != "both" {
					continue
				}
				dir := filepath.Join(parent, name)
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				for _, file := range files {
					if err := os.WriteFile(filepath.Join(dir, file), []byte("existing-work-"+file), 0600); err != nil {
						t.Fatal(err)
					}
				}
				before[name] = snapshot(dir)
			}
			log := filepath.Join(parent, "clone.log")
			cmd := exec.Command("sh", "-c", script)
			cmd.Dir = parent
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "TUTORIAL_GIT="+git, "TUTORIAL_SOURCE="+source, "TUTORIAL_CLONE_LOG="+log, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
			out, runErr := cmd.CombinedOutput()
			if runErr == nil {
				t.Fatalf("setup must exit nonzero (existing directory or intentionally failing checker):\n%s", out)
			}
			for name, want := range before {
				if got := snapshot(filepath.Join(parent, name)); !reflect.DeepEqual(got, want) {
					t.Errorf("existing %s changed", name)
				}
			}
			if existing != "fresh" {
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Errorf("setup attempted clone before refusing existing directories: %v\n%s", err, out)
				}
				return
			}
			work := filepath.Join(parent, "evener-first-task")
			for _, file := range files {
				got, err := os.ReadFile(filepath.Join(work, file))
				if err != nil {
					t.Fatal(err)
				}
				want, err := os.ReadFile(filepath.Join(examples, file))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("fresh fixture %s differs", file)
				}
			}
			verify := exec.Command(git, "ls-tree", "--name-only", "HEAD")
			verify.Dir = work
			tree, err := verify.CombinedOutput()
			if err != nil {
				t.Fatalf("fixture commit missing: %v\n%s\n%s", err, tree, out)
			}
			if got := strings.Fields(string(tree)); !reflect.DeepEqual(got, []string{"README.md", "check.py", "receipt.py"}) {
				t.Fatalf("committed files=%v", got)
			}
			check := exec.Command("python3", "check.py")
			check.Dir = work
			if err := check.Run(); err == nil || check.ProcessState == nil || check.ProcessState.ExitCode() != 1 {
				t.Fatalf("independent fixture check must exit 1: %v", err)
			}
		})
	}
}
