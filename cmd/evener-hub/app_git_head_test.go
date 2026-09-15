package hub

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubGitHeadFailSoft(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	calls := 0
	cfg := hubcore.WebConfig{ResolveGitHead: func(context.Context, string) (string, error) {
		calls++
		return "", errors.New("git unavailable")
	}}

	for _, tc := range []struct {
		name string
		cwd  string
	}{
		{name: "empty cwd", cwd: "  "},
		{name: "missing cwd", cwd: missing},
		{name: "git error", cwd: root},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := hubGitHead(context.Background(), cfg, appwire.GitHeadParams{CWD: tc.cwd})
			if got.Head != "" {
				t.Fatalf("head=%q, want empty", got.Head)
			}
		})
	}
	if calls != 1 {
		t.Fatalf("git seam calls=%d, want 1 (only the existing directory)", calls)
	}
}

func TestHubGitHeadNonGitDirectoryReturnsEmpty(t *testing.T) {
	got := hubGitHead(context.Background(), hubcore.WebConfig{}, appwire.GitHeadParams{CWD: t.TempDir(), IncludeOrigin: true})
	if got.Head != "" {
		t.Fatalf("head=%q, want empty for a non-git directory", got.Head)
	}
	if got.OriginURL != "" {
		t.Fatalf("originUrl=%q, want empty for a non-git directory", got.OriginURL)
	}
}

func TestHubGitHeadResolvesOriginAlongsideHead(t *testing.T) {
	var headDir, originDir string
	cfg := hubcore.WebConfig{
		ResolveGitHead: func(_ context.Context, dir string) (string, error) {
			headDir = dir
			return "feature/x", nil
		},
		ResolveGitOrigin: func(_ context.Context, dir string) (string, error) {
			originDir = dir
			return "git@github.com:owner/repo.git", nil
		},
	}
	got := hubGitHead(context.Background(), cfg, appwire.GitHeadParams{CWD: t.TempDir(), IncludeOrigin: true})
	if got.Head != "feature/x" {
		t.Fatalf("head=%q, want feature/x", got.Head)
	}
	if got.OriginURL != "git@github.com:owner/repo.git" {
		t.Fatalf("originUrl=%q, want the seam's origin", got.OriginURL)
	}
	if headDir == "" || headDir != originDir {
		t.Fatalf("seams saw dirs %q and %q, want the same non-empty directory", headDir, originDir)
	}
}

// Origin failure must not suppress a resolved branch: a session in a repo whose
// origin remote is unset or unreadable still shows its branch, just unlinked.
func TestHubGitHeadOriginFailureKeepsHead(t *testing.T) {
	cfg := hubcore.WebConfig{
		ResolveGitHead: func(context.Context, string) (string, error) { return "main", nil },
		ResolveGitOrigin: func(context.Context, string) (string, error) {
			return "", errors.New("no origin remote")
		},
	}
	got := hubGitHead(context.Background(), cfg, appwire.GitHeadParams{CWD: t.TempDir(), IncludeOrigin: true})
	if got.Head != "main" {
		t.Fatalf("head=%q, want main", got.Head)
	}
	if got.OriginURL != "" {
		t.Fatalf("originUrl=%q, want empty on origin failure", got.OriginURL)
	}
}

// The origin remote is opt-in: the method's other caller (the Spawn pane's
// branch chip) renders only the branch, so the hub must not even run the origin
// lookup for it - least data and one fewer git subprocess per directory change.
func TestHubGitHeadOriginIsOptIn(t *testing.T) {
	originCalls := 0
	cfg := hubcore.WebConfig{
		ResolveGitHead: func(context.Context, string) (string, error) { return "main", nil },
		ResolveGitOrigin: func(context.Context, string) (string, error) {
			originCalls++
			return "git@github.com:owner/repo.git", nil
		},
	}
	got := hubGitHead(context.Background(), cfg, appwire.GitHeadParams{CWD: t.TempDir()})
	if got.Head != "main" {
		t.Fatalf("head=%q, want main", got.Head)
	}
	if got.OriginURL != "" {
		t.Fatalf("originUrl=%q, want empty without IncludeOrigin", got.OriginURL)
	}
	if originCalls != 0 {
		t.Fatalf("origin seam calls=%d, want 0 without IncludeOrigin", originCalls)
	}
}

// `git remote get-url origin` returns the configured URL verbatim, and an https
// remote can embed a username and token. The browser needs only host and path to
// build a forge link, so the credential must be stripped before the URL crosses
// AppWire - not merely before the link is drawn.
func TestHubGitHeadStripsOriginCredentials(t *testing.T) {
	cfg := hubcore.WebConfig{
		ResolveGitHead: func(context.Context, string) (string, error) { return "main", nil },
		ResolveGitOrigin: func(context.Context, string) (string, error) {
			return "https://user:supersecret@github.com/owner/repo.git", nil
		},
	}
	got := hubGitHead(context.Background(), cfg, appwire.GitHeadParams{CWD: t.TempDir(), IncludeOrigin: true})
	if strings.Contains(got.OriginURL, "supersecret") || strings.Contains(got.OriginURL, "user:") {
		t.Fatalf("originUrl=%q leaked credentials across AppWire", got.OriginURL)
	}
	if got.OriginURL != "https://github.com/owner/repo.git" {
		t.Fatalf("originUrl=%q, want the sanitized URL", got.OriginURL)
	}
}

// HEAD and origin are independent reads: a repo with an origin remote but an
// unborn branch (fresh `git init`, an empty clone) has no HEAD yet still has an
// origin worth linking to, and the response must not be indistinguishable from a
// non-git directory.
func TestHubGitHeadOriginSurvivesHeadFailure(t *testing.T) {
	cfg := hubcore.WebConfig{
		ResolveGitHead: func(context.Context, string) (string, error) {
			return "", errors.New("unborn HEAD")
		},
		ResolveGitOrigin: func(context.Context, string) (string, error) {
			return "git@github.com:owner/repo.git", nil
		},
	}
	got := hubGitHead(context.Background(), cfg, appwire.GitHeadParams{CWD: t.TempDir(), IncludeOrigin: true})
	if got.Head != "" {
		t.Fatalf("head=%q, want empty", got.Head)
	}
	if got.OriginURL != "git@github.com:owner/repo.git" {
		t.Fatalf("originUrl=%q, want the origin resolved despite the HEAD failure", got.OriginURL)
	}
}

func TestSanitizeGitRemote(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "https credentials", in: "https://user:tok@github.com/owner/repo.git", want: "https://github.com/owner/repo.git"},
		{name: "https username only", in: "https://user@github.com/owner/repo.git", want: "https://github.com/owner/repo.git"},
		{name: "query token", in: "https://github.com/owner/repo.git?token=supersecret", want: "https://github.com/owner/repo.git"},
		{name: "fragment token", in: "https://github.com/owner/repo.git#access_token=supersecret", want: "https://github.com/owner/repo.git"},
		{name: "userinfo and query", in: "https://user:tok@github.com/owner/repo.git?token=supersecret", want: "https://github.com/owner/repo.git"},
		{name: "https clean", in: "https://github.com/owner/repo.git", want: "https://github.com/owner/repo.git"},
		{name: "ssh userinfo", in: "ssh://git@github.com/owner/repo.git", want: "ssh://github.com/owner/repo.git"},
		{name: "scp-like unchanged", in: "git@github.com:owner/repo.git", want: "git@github.com:owner/repo.git"},
		{name: "scp-like query token", in: "git@github.com:owner/repo.git?token=supersecret", want: "git@github.com:owner/repo.git"},
		{name: "scp-like fragment token", in: "git@github.com:owner/repo.git#token=supersecret", want: "git@github.com:owner/repo.git"},
		// The query is cut before the shape is judged, so a remote whose only
		// "path" was the query comes back empty rather than as the pathless
		// `git@github.com:` the frontend cannot use.
		{name: "scp-like query only", in: "git@github.com:?token=supersecret", want: ""},
		// The user and host halves take no path separator or whitespace, which
		// is what keeps a local path containing '@' out of the allowlist.
		{name: "local path containing at-sign", in: "/srv/foo@bar:baz", want: ""},
		{name: "scp-like with a space", in: "git @github.com:owner/repo.git", want: ""},
		// A local path is a remote the frontend cannot link, so it is not put on
		// the wire at all - the allowlist leaves only scp-like remotes and
		// authority-bearing URLs.
		{name: "local path", in: "/srv/git/repo.git", want: ""},
		{name: "relative path", in: "../sibling/repo.git", want: ""},
		{name: "scp-like without a path", in: "git@github.com:", want: ""},
		// The frontend needs owner AND repository to build a link, so a
		// single-segment path is not a remote worth putting on the wire.
		{name: "scp-like single segment", in: "git@github.com:repo.git", want: ""},
		{name: "scp-like without a user", in: "github.com:owner/repo.git", want: ""},
		// A scheme with no authority cannot be parsed for which part is a
		// credential, so it fails closed. Neither shape is renderable by the
		// frontend's parser (it needs `user@host:path` or an authority).
		{name: "opaque scheme with token", in: "https:token@github.com/owner/repo.git", want: ""},
		{name: "opaque file scheme", in: "file:/srv/git/repo.git", want: ""},
		// An empty authority parses with the credential in the path, not in
		// userinfo, and leaves no host to link to.
		{name: "empty authority with credentials", in: "https:///user:tok@github.com/owner/repo.git", want: ""},
		{name: "empty authority", in: "https:///github.com/owner/repo.git", want: ""},
		{name: "file scheme no authority", in: "file:///srv/git/repo.git", want: ""},
		{name: "empty", in: "   ", want: ""},
		{name: "unparseable scheme fails closed", in: "https://a b@github.com/o/r.git", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeGitRemote(tc.in); got != tc.want {
				t.Fatalf("sanitizeGitRemote(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// resolveGitOrigin's real (non-seam) path: every other test injects the seam, so
// a wrong subcommand or argument order in the real invocation would fail soft in
// production - every session silently losing its forge link - with no default
// test catching it. resolveGitHead's real path is covered the same way.
func TestResolveGitOriginRealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Isolate the fixture AND the resolver from the developer's own git
	// configuration: `git remote get-url` honours url.*.insteadOf rewrites, so
	// an ambient rule would change the URL under test.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "git@github.com:owner/repo.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	got, err := resolveGitOrigin(context.Background(), repo)
	if err != nil {
		t.Fatalf("resolveGitOrigin: %v", err)
	}
	if got != "git@github.com:owner/repo.git" {
		t.Fatalf("resolveGitOrigin=%q, want the configured origin", got)
	}

	// A directory that is not a repository has no origin to report.
	if _, err := resolveGitOrigin(context.Background(), t.TempDir()); err == nil {
		t.Fatal("resolveGitOrigin(non-repo) = nil error, want an error")
	}
}

func TestHubRPCGitHeadUsesCanonicalDirectory(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	var gotDir string
	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		Past: hubcore.NewPastIndex(""),
		ResolveGitHead: func(_ context.Context, dir string) (string, error) {
			gotDir = dir
			return "main", nil
		},
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.GitHead(context.Background(), appwire.GitHeadParams{CWD: link})
	if err != nil {
		t.Fatalf("GitHead: %v", err)
	}
	if resp.Head != "main" {
		t.Fatalf("head=%q, want main", resp.Head)
	}
	if gotDir != canonicalRoot {
		t.Fatalf("git seam directory=%q, want %q", gotDir, canonicalRoot)
	}
}
