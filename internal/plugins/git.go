package plugins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/evener/execsupport/orphanpipe"
)

var (
	gitLookPath = exec.LookPath
	gitMkdirAll = os.MkdirAll
	gitRun      = git
)

func gitAvailable() bool {
	_, err := gitLookPath("git")
	return err == nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	// Disable git remote-helper transports that execute arbitrary commands
	// (ext::/fd::), since URLs may come from untrusted marketplace manifests.
	full := append([]string{"-c", "protocol.ext.allow=never", "-c", "protocol.fd.allow=never"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	// On cancellation, terminate git gracefully where the platform allows
	// (SIGTERM — see terminateGit) instead of exec's default SIGKILL, which
	// strands git's lock files. WaitDelay hard-kills a git that has not
	// exited a few seconds later. It also bounds the output wait after git
	// exits, when something git ran (a hook from the user's core.hooksPath,
	// an ssh ProxyCommand) left a process holding the pipe (see orphanpipe).
	cmd.Cancel = func() error { return terminateGit(cmd.Process) }
	cmd.WaitDelay = 5 * time.Second
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err = orphanpipe.ChildErr(cmd, err); err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// guardGitArg rejects a value that git would parse as an option (a leading
// dash), since url/ref/sha/subdir may come from untrusted marketplace manifests.
func guardGitArg(name, val string) error {
	if strings.HasPrefix(val, "-") {
		return fmt.Errorf("refusing git %s %q: leading '-' looks like a flag", name, val)
	}
	return nil
}

// gitClone clones url into dir, then checks out ref and/or sha when set.
func gitClone(ctx context.Context, url, dir, ref, sha string) error {
	for _, g := range []struct{ n, v string }{{"url", url}, {"ref", ref}, {"sha", sha}} {
		if err := guardGitArg(g.n, g.v); err != nil {
			return err
		}
	}
	if err := gitMkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	args := []string{"clone", "--quiet"}
	if sha == "" && ref == "" {
		args = append(args, "--depth=1")
	}
	args = append(args, "--", url, dir)
	if _, err := gitRun(ctx, "", args...); err != nil {
		return err
	}
	if ref != "" {
		if _, err := gitRun(ctx, dir, "checkout", "--quiet", ref); err != nil {
			return err
		}
	}
	if sha != "" {
		if _, err := gitRun(ctx, dir, "checkout", "--quiet", sha); err != nil {
			return err
		}
	}
	return nil
}

// gitSparseClone does a blobless, sparse clone of url into dir limited to
// subdir, then pins ref/sha. Falls back to a full checkout of subdir contents.
func gitSparseClone(ctx context.Context, url, dir, subdir, ref, sha string) error {
	for _, g := range []struct{ n, v string }{{"url", url}, {"subdir", subdir}, {"ref", ref}, {"sha", sha}} {
		if err := guardGitArg(g.n, g.v); err != nil {
			return err
		}
	}
	if err := gitMkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	args := []string{"clone", "--quiet", "--filter=blob:none", "--no-checkout", "--", url, dir}
	if _, err := gitRun(ctx, "", args...); err != nil {
		return err
	}
	if _, err := gitRun(ctx, dir, "sparse-checkout", "set", "--cone", subdir); err != nil {
		return err
	}
	target := "HEAD"
	if sha != "" {
		target = sha
	} else if ref != "" {
		target = ref
	}
	if _, err := gitRun(ctx, dir, "checkout", "--quiet", target); err != nil {
		return err
	}
	return nil
}

func gitPull(ctx context.Context, dir string) error {
	_, err := gitRun(ctx, dir, "pull", "--ff-only", "--quiet")
	return err
}

// gitFetch downloads what the remote of the clone at dir has, without
// touching its checked-out files.
func gitFetch(ctx context.Context, dir string) error {
	_, err := gitRun(ctx, dir, "fetch", "--quiet")
	return err
}

// gitFastForward moves the branch checked out in the clone at dir to its
// upstream as last fetched; it needs no network.
func gitFastForward(ctx context.Context, dir string) error {
	_, err := gitRun(ctx, dir, "merge", "--ff-only", "--quiet", "@{upstream}")
	return err
}

func gitHeadSHA(ctx context.Context, dir string) (string, error) {
	out, err := gitRun(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// gitPathTree returns the id of the tree path (relative to dir) holds at the
// checked-out commit of the repository at dir. It changes exactly when the
// folder's contents do, whatever else the repository's history does: unlike
// the last commit touching the folder, it does not move when a shallow
// clone's root does (a reclone), so a reclone flags nothing.
func gitPathTree(ctx context.Context, dir, path string) (string, error) {
	out, err := gitRun(ctx, dir, "rev-parse", "--verify", "--end-of-options", "HEAD:./"+strings.TrimPrefix(filepath.ToSlash(path), "./"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// gitRemoteHead asks url, without cloning, for the commit a clone that then
// checks out ref lands on; an empty ref means the remote's HEAD. An annotated
// tag answers with its peeled commit. A tag wins over a branch of the same
// name, as git checkout resolves a name in a fresh clone. The one exception
// is not handled: checkout prefers the remote's default branch, which the
// clone already has locally, over a tag of the same name, and this still
// answers with the tag. A tag named like the default branch is too rare to be
// worth a second query.
func gitRemoteHead(ctx context.Context, url, ref string) (string, error) {
	for _, g := range []struct{ n, v string }{{"url", url}, {"ref", ref}} {
		if err := guardGitArg(g.n, g.v); err != nil {
			return "", err
		}
	}
	if ref == "" {
		ref = "HEAD"
	}
	out, err := gitRun(ctx, "", "ls-remote", "--", url, ref, ref+"^{}")
	if err != nil {
		return "", err
	}
	shas := map[string]string{}
	for line := range strings.SplitSeq(out, "\n") {
		if sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
			shas[name] = sha
		}
	}
	// A short name is looked up as a tag, then a branch; a full refname (or
	// HEAD) matches itself. Either way a peeled line beats the tag object.
	for _, name := range []string{"refs/tags/" + ref + "^{}", "refs/tags/" + ref, "refs/heads/" + ref, ref + "^{}", ref} {
		if sha := shas[name]; sha != "" {
			return sha, nil
		}
	}
	return "", fmt.Errorf("git ls-remote: %s has no ref %q", url, ref)
}
