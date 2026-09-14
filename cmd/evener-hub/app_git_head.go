package hub

import (
	"context"
	"os/exec"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

var gitCommand = exec.CommandContext

// hubGitHead resolves a working directory's git HEAD and "origin" remote URL
// for the hub AppWire method. Git and filesystem failures are intentionally
// represented as empty values: callers use these only as display metadata
// (the Spawn pane's branch chip, the composer's location line).
func hubGitHead(ctx context.Context, cfg hubcore.WebConfig, params appwire.GitHeadParams) appwire.GitHeadResponse {
	cwd, err := fspaths.CanonicalizeDir(params.CWD)
	if err != nil {
		return appwire.GitHeadResponse{}
	}

	resolve := resolveGitHead
	if cfg.ResolveGitHead != nil {
		resolve = cfg.ResolveGitHead
	}
	head, err := resolve(ctx, cwd)
	if err != nil {
		return appwire.GitHeadResponse{}
	}

	// Origin resolution is independent of HEAD: a detached HEAD or a repo with
	// no branch still has an origin worth linking to, and a missing origin
	// never suppresses the branch. Both failures collapse to "".
	resolveOrigin := resolveGitOrigin
	if cfg.ResolveGitOrigin != nil {
		resolveOrigin = cfg.ResolveGitOrigin
	}
	origin, err := resolveOrigin(ctx, cwd)
	if err != nil {
		origin = ""
	}
	return appwire.GitHeadResponse{Head: head, OriginURL: origin}
}

// resolveGitHead returns the current branch name or, in detached HEAD state,
// the short commit SHA.
func resolveGitHead(ctx context.Context, dir string) (string, error) {
	out, err := gitCommand(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(out))
	if head != "HEAD" {
		return head, nil
	}
	out, err = gitCommand(ctx, "git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return head, nil //nolint:nilerr // the literal "HEAD" remains a valid best-effort result
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveGitOrigin returns the "origin" remote URL for the repo at dir, or an
// error when dir is not a git repo or has no origin remote configured.
func resolveGitOrigin(ctx context.Context, dir string) (string, error) {
	out, err := gitCommand(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
