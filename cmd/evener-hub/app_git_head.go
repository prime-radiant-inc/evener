package hub

import (
	"context"
	"net/url"
	"os/exec"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

var gitCommand = exec.CommandContext

// hubGitHead resolves a working directory's git HEAD and, when the caller asks
// for it (GitHeadParams.IncludeOrigin), its "origin" remote URL. Git and
// filesystem failures are intentionally represented as empty values: callers
// use these only as display metadata (the Spawn pane's branch chip, the
// composer's location line).
func hubGitHead(ctx context.Context, cfg hubcore.WebConfig, params appwire.GitHeadParams) appwire.GitHeadResponse {
	cwd, err := fspaths.CanonicalizeDir(params.CWD)
	if err != nil {
		return appwire.GitHeadResponse{}
	}

	resolve := resolveGitHead
	if cfg.ResolveGitHead != nil {
		resolve = cfg.ResolveGitHead
	}
	// HEAD and origin are independent reads, and each failure collapses only
	// its own half. A repo with an origin remote but no HEAD yet (a fresh
	// `git init`, an empty clone) still has an origin worth linking to, and a
	// detached HEAD or a missing origin never suppresses the branch.
	head, headErr := resolve(ctx, cwd)
	if headErr != nil {
		head = ""
	}

	// Origin is opt-in: a branch-only caller gets neither the lookup nor the
	// remote on the wire.
	origin := ""
	if params.IncludeOrigin {
		resolveOrigin := resolveGitOrigin
		if cfg.ResolveGitOrigin != nil {
			resolveOrigin = cfg.ResolveGitOrigin
		}
		if raw, originErr := resolveOrigin(ctx, cwd); originErr == nil {
			origin = sanitizeGitRemote(raw)
		}
	}
	return appwire.GitHeadResponse{Head: head, OriginURL: origin}
}

// sanitizeGitRemote strips credentials from a remote URL before it crosses
// AppWire. `git remote get-url origin` returns the configured URL verbatim, and
// an https remote can embed a username and token
// (https://user:token@host/owner/repo.git); the browser needs only the host and
// path to build a forge link, so the userinfo is removed here rather than left
// for the caller to drop when it draws the link.
//
// A scheme-less, scp-like remote (git@host:owner/repo.git) is returned
// unchanged: its leading `user@` is part of the address rather than a
// credential, and the frontend's parser needs it. Anything with a scheme that
// fails to parse yields "" - a URL this function cannot inspect is one it
// cannot promise is credential-free.
func sanitizeGitRemote(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.Contains(trimmed, "://") {
		return trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	parsed.User = nil
	return parsed.String()
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
