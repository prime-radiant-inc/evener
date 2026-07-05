package plugins

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	// Disable git remote-helper transports that execute arbitrary commands
	// (ext::/fd::), since URLs may come from untrusted marketplace manifests.
	full := append([]string{"-c", "protocol.ext.allow=never", "-c", "protocol.fd.allow=never"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
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
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	args := []string{"clone", "--quiet"}
	if sha == "" && ref == "" {
		args = append(args, "--depth=1")
	}
	args = append(args, "--", url, dir)
	if _, err := git(ctx, "", args...); err != nil {
		return err
	}
	if ref != "" {
		if _, err := git(ctx, dir, "checkout", "--quiet", ref); err != nil {
			return err
		}
	}
	if sha != "" {
		if _, err := git(ctx, dir, "checkout", "--quiet", sha); err != nil {
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
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	args := []string{"clone", "--quiet", "--filter=blob:none", "--no-checkout", "--", url, dir}
	if _, err := git(ctx, "", args...); err != nil {
		return err
	}
	if _, err := git(ctx, dir, "sparse-checkout", "set", "--cone", subdir); err != nil {
		return err
	}
	target := "HEAD"
	if sha != "" {
		target = sha
	} else if ref != "" {
		target = ref
	}
	if _, err := git(ctx, dir, "checkout", "--quiet", target); err != nil {
		return err
	}
	return nil
}

func gitPull(ctx context.Context, dir string) error {
	_, err := git(ctx, dir, "pull", "--ff-only", "--quiet")
	return err
}

func gitHeadSHA(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
