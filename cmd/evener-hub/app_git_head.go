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
// a remote can carry a secret in more than one place: userinfo
// (https://user:token@host/owner/repo.git), a query string
// (https://host/owner/repo.git?token=secret), or a fragment. The browser needs
// only scheme, host and path to build a forge link, so every other part is
// dropped here rather than left for the caller to ignore when it draws the
// link.
//
// A scheme-less, scp-like remote (git@host:owner/repo.git) is returned
// unchanged: its leading `user@` is part of the address rather than a
// credential, and the frontend's parser needs it. Anything else yields "" - a
// URL this function cannot inspect is one it cannot promise is credential-free,
// and a local path is a remote the frontend cannot turn into a link anyway.
// The net contract is an allowlist: only an authority-bearing URL (sanitized)
// or an scp-like remote leaves here.
func sanitizeGitRemote(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.Contains(trimmed, "://") {
		// A scheme with no authority (https:token@host/path) is a shape this
		// function cannot parse: there is no way to tell which part of it is a
		// credential, so it fails closed exactly like an unparseable scheme
		// URL. Nothing renderable is lost - the frontend's parser only
		// recognizes `user@host:path` or a scheme URL with an authority.
		if hasOpaqueScheme(trimmed) {
			return ""
		}
		// A query or fragment is not part of a git remote address, and a
		// `?token=...` there would otherwise cross AppWire untouched: the
		// scheme-less branch cannot rely on url.Parse to drop it. Cutting first
		// also keeps the shape check below honest: `git@host:?token=...` must be
		// judged on the pathless remainder it actually is.
		if cut := strings.IndexAny(trimmed, "?#"); cut >= 0 {
			trimmed = trimmed[:cut]
		}
		// Local paths and bare hostnames are not remotes the frontend can link;
		// putting them on the wire buys nothing.
		if !looksLikeScpRemote(trimmed) {
			return ""
		}
		return trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	// A scheme with an empty authority (https:///user:token@host/path) parses
	// with the credential sitting in the PATH rather than in userinfo, so
	// clearing User would leave it in place. There is no host to build a link
	// from either, so this fails closed like the other opaque shapes.
	if parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

// looksLikeScpRemote reports whether value is git's scheme-less scp-like remote
// syntax, `user@host:path`. It mirrors the frontend parser's own SCP_LIKE
// pattern - a user with no path separator or whitespace, a host with no
// separator, and a non-empty path - so the hub never emits a shape the browser
// cannot turn into a link, and never mistakes a local path for a remote.
func looksLikeScpRemote(value string) bool {
	const separatorOrSpace = "/ \t\n\v\f\r"
	user, rest, found := strings.Cut(value, "@")
	if !found || user == "" || strings.ContainsAny(user, separatorOrSpace) {
		return false
	}
	host, path, found := strings.Cut(rest, ":")
	if !found || host == "" || strings.ContainsAny(host, separatorOrSpace) {
		return false
	}
	return strings.TrimSpace(path) != ""
}

// hasOpaqueScheme reports whether value starts with a URL scheme (RFC 3986:
// ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ) ":") and so names a URL whose
// authority this package never got to parse - the caller sees a scheme-less
// string only because the "//" is missing. A scp-like remote's `user@host:`
// prefix cannot match: '@' and ':' are not scheme characters.
func hasOpaqueScheme(value string) bool {
	colon := strings.IndexByte(value, ':')
	if colon <= 0 {
		return false
	}
	for i := range colon {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
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
