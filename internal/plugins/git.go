package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/evener/execsupport/orphanpipe"
	"primeradiant.com/evener/identifier"
)

var (
	gitLookPath = exec.LookPath
	gitMkdirAll = os.MkdirAll
	gitRun      = git
	gitStdout   = gitStdoutImpl
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
	// Repository-selection variables (GIT_DIR, GIT_WORK_TREE, and friends) name
	// an ambient repository that has nothing to do with the path this command
	// operates on. Strip them so the command works on the repository its own
	// directory or arguments name. Other Git configuration, credentials, hooks,
	// and transports stay intact.
	cmd.Env = append(identifier.FilteredGitEnvironment(os.Environ()), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err = orphanpipe.ChildErr(cmd, err); err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// gitStdout answers with git's standard output alone, keeping diagnostic
// writes to standard error (trace lines, warnings) out of the answer.
func gitStdoutImpl(ctx context.Context, dir string, args ...string) (string, error) {
	// Keep the same command shape as git: the transport guards, the same
	// directory and environment, and the same cancellation behavior.
	full := append([]string{"-c", "protocol.ext.allow=never", "-c", "protocol.fd.allow=never"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Cancel = func() error { return terminateGit(cmd.Process) }
	cmd.WaitDelay = 5 * time.Second
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(identifier.FilteredGitEnvironment(os.Environ()), "GIT_TERMINAL_PROMPT=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err = orphanpipe.ChildErr(cmd, err); err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
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

// validateRef rejects a fully qualified ref and a flag-like value. Source refs
// are branch or tag names; accepting refs/... would make checkout, refresh,
// and remote lookup disagree about which ref the name selects.
func validateRef(name, val string) error {
	if strings.HasPrefix(val, "refs/") {
		return fmt.Errorf("%w: git %s %q", ErrSourceRefQualified, name, val)
	}
	return guardGitArg(name, val)
}

// checkoutRevision checks out target only after proving it names a commit.
// git checkout treats an unmatched name as a pathspec, so without this a ref
// that matches a tracked file or directory would leave the clone on its
// default branch while the caller records the ref as checked out.
func checkoutRevision(ctx context.Context, dir, target string) error {
	if target == "HEAD" {
		_, err := gitRun(ctx, dir, "checkout", "--quiet", "HEAD")
		return err
	}
	// Name each namespace explicitly so a path cannot satisfy the lookup, and
	// check out the resolved shape: a local branch stays attached, a remote
	// branch gets a tracking branch without relying on checkout.guess, and a
	// tag or commit detaches.
	resolved, err := resolveRef(ctx, dir, target)
	if err != nil {
		return err
	}
	switch resolved.kind {
	case refLocalBranch:
		_, err = gitRun(ctx, dir, "checkout", "--quiet", resolved.name)
	case refTag:
		_, err = gitRun(ctx, dir, "checkout", "--quiet", "--detach", resolved.ref)
	case refRemoteBranch:
		_, err = gitRun(ctx, dir, "checkout", "--quiet", "-b", resolved.name, "--track", resolved.ref)
	default:
		_, err = gitRun(ctx, dir, "checkout", "--quiet", "--detach", resolved.ref)
	}
	return err
}

type refKind int

const (
	refLocalBranch refKind = iota
	refTag
	refRemoteBranch
	refRevision
)

type resolvedRef struct {
	ref  string
	name string
	kind refKind
}

// resolveRef classifies target against the namespaces checkout may use and
// answers the ref to check out or compare. Checkout and pin verification share
// it so a target one accepts is not read as broken by the other.
func resolveRef(ctx context.Context, dir, target string) (resolvedRef, error) {
	if target == "HEAD" {
		return resolvedRef{ref: "HEAD", name: "HEAD", kind: refRevision}, nil
	}
	switch {
	case exactRefNamesCommit(ctx, dir, "refs/heads/"+target):
		return resolvedRef{ref: "refs/heads/" + target, name: target, kind: refLocalBranch}, nil
	case exactRefNamesCommit(ctx, dir, "refs/tags/"+target):
		return resolvedRef{ref: "refs/tags/" + target, name: target, kind: refTag}, nil
	case exactRefNamesCommit(ctx, dir, "refs/remotes/origin/"+target):
		return resolvedRef{ref: "refs/remotes/origin/" + target, name: target, kind: refRemoteBranch}, nil
	case revNamesCommit(ctx, dir, target):
		return resolvedRef{ref: target, name: target, kind: refRevision}, nil
	default:
		return resolvedRef{}, fmt.Errorf("refusing to check out %q: it does not name a branch, tag, or commit", target)
	}
}

// exactRefNamesCommit reports whether ref exists in exactly the namespace
// named. show-ref, unlike rev-parse, does not expand an input like
// refs/heads/x to a tag or another namespace that happens to share the name.
func exactRefNamesCommit(ctx context.Context, dir, ref string) bool {
	exists, err := gitRefExists(ctx, dir, ref)
	if err != nil || !exists {
		return false
	}
	_, err = gitRun(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// checkoutCommit checks out the commit named by sha itself. It resolves the
// prefix against object names only, so a tag or branch that shares the prefix
// cannot stand in for the commit.
func checkoutCommit(ctx context.Context, dir, sha string) error {
	out, err := gitStdout(ctx, dir, "rev-parse", "--disambiguate="+sha)
	if err != nil {
		return fmt.Errorf("refusing to check out %q: it does not name a commit", sha)
	}
	ids := strings.Fields(out)
	if len(ids) != 1 {
		return fmt.Errorf("refusing to check out %q: it does not name exactly one commit", sha)
	}
	kind, err := gitStdout(ctx, dir, "cat-file", "-t", ids[0])
	if err != nil || strings.TrimSpace(kind) != "commit" {
		return fmt.Errorf("refusing to check out %q: it does not name a commit", sha)
	}
	_, err = gitRun(ctx, dir, "checkout", "--quiet", "--detach", ids[0])
	return err
}

func revNamesCommit(ctx context.Context, dir, rev string) bool {
	_, err := gitRun(ctx, dir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	return err == nil
}

// gitClone clones url into dir, then checks out ref and/or sha when set.
func gitClone(ctx context.Context, url, dir, ref, sha string) error {
	if err := guardGitArg("url", url); err != nil {
		return err
	}
	if err := validateRef("ref", ref); err != nil {
		return err
	}
	if err := validateRef("sha", sha); err != nil {
		return err
	}
	if err := gitMkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	args := []string{"clone", "--quiet", "--origin", "origin"}
	if sha == "" && ref == "" {
		args = append(args, "--depth=1")
	}
	args = append(args, "--", url, dir)
	if _, err := gitRun(ctx, "", args...); err != nil {
		return err
	}
	if ref != "" {
		if err := checkoutRevision(ctx, dir, ref); err != nil {
			return err
		}
	}
	if sha != "" {
		if err := checkoutCommit(ctx, dir, sha); err != nil {
			return err
		}
	}
	return nil
}

// gitSparseClone does a blobless, sparse clone of url into dir limited to
// subdir, then pins ref/sha. Falls back to a full checkout of subdir contents.
func gitSparseClone(ctx context.Context, url, dir, subdir, ref, sha string) error {
	if err := guardGitArg("url", url); err != nil {
		return err
	}
	if err := guardGitArg("subdir", subdir); err != nil {
		return err
	}
	if err := validateRef("ref", ref); err != nil {
		return err
	}
	if err := validateRef("sha", sha); err != nil {
		return err
	}
	if err := gitMkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	args := []string{"clone", "--quiet", "--origin", "origin", "--filter=blob:none", "--no-checkout", "--", url, dir}
	if _, err := gitRun(ctx, "", args...); err != nil {
		return err
	}
	if _, err := gitRun(ctx, dir, "sparse-checkout", "set", "--cone", subdir); err != nil {
		return err
	}
	switch {
	case sha != "":
		return checkoutCommit(ctx, dir, sha)
	case ref != "":
		return checkoutRevision(ctx, dir, ref)
	default:
		return checkoutRevision(ctx, dir, "HEAD")
	}
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
// upstream as last fetched. It needs no network, except that a blobless
// clone downloads the contents of the files it changes.
func gitFastForward(ctx context.Context, dir string) error {
	_, err := gitRun(ctx, dir, "merge", "--ff-only", "--quiet", "@{upstream}")
	return err
}

// gitRefNamesBranch reports whether name, checked out in the clone at dir,
// names a branch rather than a tag or a commit: whether checkout left a local
// branch of that name. A fresh clone has one only for its remote's default
// branch; checkout makes one for any other branch unless a tag of the same
// name shadows it, and detaches at the tag instead.
func gitRefNamesBranch(ctx context.Context, dir, name string) (bool, error) {
	if err := guardGitArg("ref", name); err != nil {
		return false, err
	}
	return gitRefExists(ctx, dir, "refs/heads/"+name)
}

// gitRefExists reports whether ref exists in the clone at dir. Git's exit
// status 1 says it does not; any other failure is an error.
func gitRefExists(ctx context.Context, dir, ref string) (bool, error) {
	_, err := gitRun(ctx, dir, "show-ref", "--verify", "--quiet", ref)
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// gitResolveCommit answers the commit rev names in the clone at dir.
func gitResolveCommit(ctx context.Context, dir, rev string) (string, error) {
	if err := guardGitArg("ref", rev); err != nil {
		return "", err
	}
	resolved, err := resolveRef(ctx, dir, rev)
	if err != nil {
		return "", err
	}
	out, err := gitRun(ctx, dir, "rev-parse", "--verify", "--quiet", resolved.ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
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
	if err := guardGitArg("url", url); err != nil {
		return "", err
	}
	if err := validateRef("ref", ref); err != nil {
		return "", err
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
	// A branch or tag name is looked up as a tag, then a branch; HEAD matches
	// itself. Either way a peeled line beats the tag object.
	for _, name := range []string{"refs/tags/" + ref + "^{}", "refs/tags/" + ref, "refs/heads/" + ref, ref + "^{}", ref} {
		if sha := shas[name]; sha != "" {
			return sha, nil
		}
	}
	return "", fmt.Errorf("git ls-remote: %s has no ref %q", url, ref)
}
