// A git origin URL is the only place a session's forge identity lives: the
// branch alone says where you are in the repo, but not which repo page to open.
// This module turns the raw `git remote get-url origin` string into the pieces
// the composer's location line needs, and returns null for anything it cannot
// honestly resolve to a forge web page.
//
// Deliberately narrow: only the three forges whose repo page is a plain
// `https://<host>/<owner>/<repo>` are recognized. A self-hosted forge or a
// private server gets null - the location line then shows the branch unlinked,
// rather than inventing a URL shape that host may not serve.
//
// Three URL spellings reach here in practice:
//   - scp-like:  git@github.com:owner/repo.git
//   - explicit:  ssh://git@github.com/owner/repo.git
//   - https:     https://github.com/owner/repo.git
// Credentials in an https URL (`https://user:token@host/...`) are ignored: the
// link is to the public repo page, never to a credentialed URL.

export type Forge = "github" | "gitlab" | "bitbucket";

export interface RepoRemote {
  forge: Forge;
  host: string;
  // owner may contain slashes: a GitLab subgroup path is `group/subgroup`.
  owner: string;
  repo: string;
  // The forge's repo page: https://<host>/<owner>/<repo>, `.git` stripped.
  repoUrl: string;
}

const FORGE_HOSTS: ReadonlyMap<string, Forge> = new Map([
  ["github.com", "github"],
  ["gitlab.com", "gitlab"],
  ["bitbucket.org", "bitbucket"],
]);

// The forge's own name, for prose (the branch link's tooltip). Kept beside the
// host map so a new forge cannot be recognized without also being nameable.
export const FORGE_LABELS: Readonly<Record<Forge, string>> = {
  github: "GitHub",
  gitlab: "GitLab",
  bitbucket: "Bitbucket",
};

// SCP-like syntax carries no URL scheme: [user@]host:path. Requiring the `@`
// keeps a bare `C:\repo` from being read as host `C` with path `\repo`, and
// every real git remote in this form has a user (usually `git`).
const SCP_LIKE = /^[^/@\s]+@([^/:\s]+):(.+)$/;

// A URL scheme at the very start (RFC 3986: ALPHA *( ALPHA / DIGIT / "+" / "-"
// / "." ) ":"). Used only to reject a scheme with no authority; a scp-like
// `user@host:` prefix cannot match, since '@' is not a scheme character.
const OPAQUE_SCHEME = /^[A-Za-z][A-Za-z0-9+.-]*:/;

function stripGitSuffix(segment: string): string {
  return segment.endsWith(".git") ? segment.slice(0, -".git".length) : segment;
}

// Host and path from either spelling, or null when the input is not a remote
// URL at all (empty, a local path, a bare word).
function splitHostAndPath(origin: string): { host: string; path: string } | null {
  const scp = SCP_LIKE.exec(origin);
  if (scp) {
    const host = scp[1];
    const path = scp[2];
    // The regex guarantees both groups, but the compiler cannot see that.
    if (host === undefined || path === undefined) return null;
    return { host, path };
  }
  let url: URL;
  try {
    url = new URL(origin);
  } catch {
    return null;
  }
  if (url.hostname === "") return null;
  return { host: url.hostname, path: url.pathname };
}

export function parseRepoRemote(origin: string): RepoRemote | null {
  const trimmed = origin.trim();
  if (trimmed === "") return null;

  // Defense in depth for a credential that arrives by a path the hub's own
  // sanitizer cannot see (it strips userinfo, query and fragment before the
  // remote crosses AppWire, but a parser should never trust that). A query or
  // fragment is never part of the repo address, and `?token=...` would
  // otherwise end up inside repoUrl and therefore in the DOM.
  const cut = trimmed.search(/[?#]/);
  const clean = cut >= 0 ? trimmed.slice(0, cut) : trimmed;
  if (clean === "") return null;

  // A scheme with no authority (`https:cred@host/path`) is the shape the hub
  // rejects outright: neither side can tell which part of it is a credential.
  // Reject it here too rather than reconstruct a link from it.
  if (!clean.includes("://") && OPAQUE_SCHEME.test(clean)) return null;

  const split = splitHostAndPath(clean);
  if (!split) return null;

  const host = split.host.toLowerCase();
  const forge = FORGE_HOSTS.get(host);
  if (!forge) return null;

  const segments = split.path.split("/").filter((segment) => segment !== "");
  if (segments.length < 2) return null;

  const lastSegment = segments[segments.length - 1];
  if (lastSegment === undefined) return null;
  const repo = stripGitSuffix(lastSegment);
  if (repo === "") return null;
  const owner = segments.slice(0, -1).join("/");

  return { forge, host, owner, repo, repoUrl: `https://${host}/${owner}/${repo}` };
}
