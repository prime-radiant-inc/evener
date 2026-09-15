import { expect, test } from "vitest";
import { parseRepoRemote } from "./repoRemote";

// Every accepted spelling of the same remote resolves to the same repo page.
// The three forms are what `git remote get-url origin` actually returns
// depending on how the clone was made, so all three must land identically.
test("scp-like, ssh:// and https spellings resolve to the same repo page", () => {
  const want = {
    forge: "github",
    host: "github.com",
    owner: "owner",
    repo: "repo",
    repoUrl: "https://github.com/owner/repo",
  };
  expect(parseRepoRemote("git@github.com:owner/repo.git")).toEqual(want);
  expect(parseRepoRemote("ssh://git@github.com/owner/repo.git")).toEqual(want);
  expect(parseRepoRemote("https://github.com/owner/repo.git")).toEqual(want);
});

test("identifies each supported forge by host", () => {
  expect(parseRepoRemote("git@gitlab.com:group/repo.git")?.forge).toBe("gitlab");
  expect(parseRepoRemote("git@bitbucket.org:team/repo.git")?.forge).toBe("bitbucket");
  expect(parseRepoRemote("git@github.com:o/r.git")?.forge).toBe("github");
});

test("keeps a GitLab subgroup path as the owner", () => {
  expect(parseRepoRemote("git@gitlab.com:group/subgroup/repo.git")).toEqual({
    forge: "gitlab",
    host: "gitlab.com",
    owner: "group/subgroup",
    repo: "repo",
    repoUrl: "https://gitlab.com/group/subgroup/repo",
  });
});

test("strips a trailing .git and a trailing slash", () => {
  expect(parseRepoRemote("https://github.com/owner/repo.git")?.repoUrl).toBe("https://github.com/owner/repo");
  expect(parseRepoRemote("https://github.com/owner/repo")?.repoUrl).toBe("https://github.com/owner/repo");
  expect(parseRepoRemote("https://github.com/owner/repo.git/")?.repoUrl).toBe("https://github.com/owner/repo");
});

// The link is to the public repo page, so credentials embedded in an https
// remote must never reach it.
test("drops embedded credentials from an https remote", () => {
  expect(parseRepoRemote("https://user:token@github.com/owner/repo.git")).toEqual({
    forge: "github",
    host: "github.com",
    owner: "owner",
    repo: "repo",
    repoUrl: "https://github.com/owner/repo",
  });
});

test("lowercases the host so a mixed-case remote still matches its forge", () => {
  expect(parseRepoRemote("git@GitHub.com:Owner/Repo.git")).toEqual({
    forge: "github",
    host: "github.com",
    owner: "Owner",
    repo: "Repo",
    repoUrl: "https://github.com/Owner/Repo",
  });
});

// A remote can carry a secret in its query or fragment, not only in userinfo.
// The hub strips these before sending, and the parser drops them too rather
// than trusting its input: otherwise the token would land inside repoUrl and
// therefore in the DOM.
test("drops a query or fragment that could carry a token", () => {
  expect(parseRepoRemote("https://github.com/owner/repo.git?token=supersecret")?.repoUrl).toBe(
    "https://github.com/owner/repo",
  );
  expect(parseRepoRemote("git@github.com:owner/repo.git?token=supersecret")?.repoUrl).toBe(
    "https://github.com/owner/repo",
  );
  expect(parseRepoRemote("git@gitlab.com:group/repo.git#access_token=supersecret")?.repoUrl).toBe(
    "https://gitlab.com/group/repo",
  );
  expect(parseRepoRemote("https://github.com/owner/repo.git?token=supersecret")?.repo).toBe("repo");
  // Nothing but a query is not a remote at all.
  expect(parseRepoRemote("?token=supersecret")).toBeNull();
});

// An opaque scheme form (`https:token@host/path`, no `//`) is neither a scheme
// URL the parser can read nor something it should trust, and the hub rejects it
// outright. This side matches that contract rather than reconstructing a link
// from input neither side can fully inspect.
test("rejects an opaque scheme form", () => {
  expect(parseRepoRemote("https:supersecret@github.com/owner/repo.git")).toBeNull();
  expect(parseRepoRemote("file:/srv/git/repo.git")).toBeNull();
});

// An empty authority (`https:///user:tok@host/path`) is rejected by the hub's
// parser, which sees no host; the browser's parser collapses the slashes and
// reads a real host. Either way the credential must never reach repoUrl, which
// is the property that matters on this side.
test("never carries a credential from an empty-authority form into the repo url", () => {
  const emptyAuthority = parseRepoRemote("https:///user:tok@github.com/owner/repo.git");
  expect(emptyAuthority?.repoUrl).toBe("https://github.com/owner/repo");
  expect(emptyAuthority?.repoUrl).not.toContain("tok");
});

// Anything not a known forge's repo page yields null: the caller then shows the
// branch unlinked rather than guessing a URL shape.
test("returns null for unknown hosts, local paths and non-remotes", () => {
  for (const origin of [
    "",
    "   ",
    "git@code.example.com:team/repo.git",
    "https://git.example.com/team/repo.git",
    "/srv/git/repo.git",
    "file:///srv/git/repo.git",
    "github.com",
    "git@github.com:repo.git",
    "git@github.com:",
  ]) {
    expect(parseRepoRemote(origin), origin).toBeNull();
  }
});
