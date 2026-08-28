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

// hubGitHead resolves a working directory's git HEAD for the hub AppWire
// method. Git and filesystem failures are intentionally represented as an
// empty HEAD: the Spawn pane uses this value only as display metadata.
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
	return appwire.GitHeadResponse{Head: head}
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
