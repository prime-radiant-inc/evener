import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import { ClientProvider } from "../../../shell/clientContext";
import { RepoLocation } from "./RepoLocation";

afterEach(cleanup);

function renderLocation(cwd: string, client: FakeClient, local = true) {
  return render(
    <ClientProvider client={client}>
      <RepoLocation cwd={cwd} local={local} />
    </ClientProvider>,
  );
}

function clientReporting(result: { head: string; originUrl?: string }): FakeClient {
  const client = new FakeClient();
  client.on("evener/git/head", () => result);
  return client;
}

test("renders nothing for an empty cwd, without asking the hub", () => {
  const client = new FakeClient();
  const { container } = renderLocation("  ", client);
  expect(container.querySelector('[data-testid="composer-repo-location"]')).toBeNull();
  expect(client.calls).toHaveLength(0);
});

// A source-backed session's cwd is another host's path: resolving it here could
// surface an unrelated local repository that happens to share it, so the working
// dir is shown alone.
test("shows only the working dir for a non-local session, without a git lookup", () => {
  const client = new FakeClient();
  renderLocation("/srv/remote/repo", client, false);

  expect(screen.getByTestId("composer-repo-path").textContent).toBe("/srv/remote/repo");
  expect(screen.queryByTestId("composer-repo-branch")).toBeNull();
  expect(screen.queryByTestId("composer-repo-link")).toBeNull();
  expect(client.calls).toHaveLength(0);
});

test("names the repository as owner/repo#branch and links it to the forge page", async () => {
  const client = clientReporting({ head: "feature/x", originUrl: "git@github.com:owner/repo.git" });
  renderLocation("/home/jesse/repo", client);

  expect((await screen.findByTestId("composer-repo-path")).textContent).toBe("/home/jesse/repo");
  const link = await screen.findByTestId("composer-repo-link");
  expect(link.getAttribute("href")).toBe("https://github.com/owner/repo");
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener noreferrer");
  expect(screen.getByTestId("composer-repo-ref").textContent).toBe("owner/repo#feature/x");
  expect(screen.getByTestId("forge-mark").getAttribute("data-forge")).toBe("github");
  // The bare branch is only for the no-forge case; here the repo carries it.
  expect(screen.queryByTestId("composer-repo-branch")).toBeNull();
});

test("recognizes GitLab and Bitbucket origins", async () => {
  const gitlab = clientReporting({ head: "main", originUrl: "git@gitlab.com:group/sub/repo.git" });
  const { unmount } = renderLocation("/repo", gitlab);
  expect((await screen.findByTestId("composer-repo-link")).getAttribute("href")).toBe(
    "https://gitlab.com/group/sub/repo",
  );
  // A GitLab subgroup path is part of the owner, exactly as the URL says.
  expect(screen.getByTestId("composer-repo-ref").textContent).toBe("group/sub/repo#main");
  expect(screen.getByTestId("forge-mark").getAttribute("data-forge")).toBe("gitlab");
  unmount();

  const bitbucket = clientReporting({ head: "main", originUrl: "https://bitbucket.org/team/repo.git" });
  renderLocation("/repo", bitbucket);
  expect((await screen.findByTestId("composer-repo-link")).getAttribute("href")).toBe(
    "https://bitbucket.org/team/repo",
  );
  expect(screen.getByTestId("composer-repo-ref").textContent).toBe("team/repo#main");
  expect(screen.getByTestId("forge-mark").getAttribute("data-forge")).toBe("bitbucket");
});

// A forge the web UI does not know, or a repo with no origin remote: the branch
// is still worth showing, just not as a link to a URL shape we would be
// guessing at.
test("shows the branch unlinked when the origin is not a recognized forge", async () => {
  const client = clientReporting({ head: "main", originUrl: "git@code.example.com:team/repo.git" });
  renderLocation("/repo", client);

  expect((await screen.findByTestId("composer-repo-branch")).textContent).toBe("main");
  expect(screen.queryByTestId("composer-repo-link")).toBeNull();
  expect(screen.queryByTestId("forge-mark")).toBeNull();
});

test("shows the branch unlinked when the repo has no origin remote", async () => {
  const client = clientReporting({ head: "main" });
  renderLocation("/repo", client);

  expect((await screen.findByTestId("composer-repo-branch")).textContent).toBe("main");
  expect(screen.queryByTestId("composer-repo-link")).toBeNull();
});

// Not a git repo (or a failed lookup): the cwd is still a fact worth showing,
// and nothing here may make the composer look broken.
test("shows only the cwd when no branch resolves", async () => {
  const client = clientReporting({ head: "" });
  renderLocation("/tmp/plain", client);

  expect((await screen.findByTestId("composer-repo-path")).textContent).toBe("/tmp/plain");
  await waitFor(() => expect(client.calls).toHaveLength(1));
  expect(screen.queryByTestId("composer-repo-branch")).toBeNull();
  expect(screen.queryByTestId("composer-repo-link")).toBeNull();
});

test("fails soft when the hub request rejects", async () => {
  const client = new FakeClient();
  client.on("evener/git/head", () => {
    throw new Error("hub unavailable");
  });
  renderLocation("/repo", client);

  expect((await screen.findByTestId("composer-repo-path")).textContent).toBe("/repo");
  await waitFor(() => expect(client.calls).toHaveLength(1));
  expect(screen.queryByTestId("composer-repo-branch")).toBeNull();
});

// A response that arrives after the composer has moved to a different cwd must
// not be displayed against the new directory.
test("ignores a late response for a cwd the composer has left", async () => {
  const client = new FakeClient();
  let resolveFirst: ((value: { head: string; originUrl: string }) => void) | undefined;
  client.on("evener/git/head", ({ cwd }) =>
    cwd === "/first"
      ? new Promise((resolve) => {
          resolveFirst = resolve;
        })
      : { head: "second-branch" },
  );

  const { rerender } = renderLocation("/first", client);
  rerender(
    <ClientProvider client={client}>
      <RepoLocation cwd="/second" local />
    </ClientProvider>,
  );
  expect((await screen.findByTestId("composer-repo-branch")).textContent).toBe("second-branch");

  resolveFirst?.({ head: "first-branch", originUrl: "" });
  // The stale value must never replace the current one.
  await waitFor(() => expect(screen.getByTestId("composer-repo-branch").textContent).toBe("second-branch"));
});

// A hub switch replaces the client. The previous hub's branch must not stay on
// screen while the new lookup is in flight (or if it never answers) - that
// branch describes a different hub.
test("drops the previous client's branch after the client is replaced", async () => {
  const oldClient = clientReporting({ head: "old-branch", originUrl: "git@github.com:owner/repo.git" });
  const newClient = new FakeClient();
  // Never answers: the point is what is displayed while the new lookup is
  // unresolved.
  newClient.on("evener/git/head", () => new Promise<never>(() => {}));

  const { rerender } = renderLocation("/repo", oldClient);
  expect((await screen.findByTestId("composer-repo-ref")).textContent).toBe("owner/repo#old-branch");

  rerender(
    <ClientProvider client={newClient}>
      <RepoLocation cwd="/repo" local />
    </ClientProvider>,
  );
  expect(screen.queryByTestId("composer-repo-branch")).toBeNull();
  expect(screen.queryByTestId("composer-repo-ref")).toBeNull();
  expect(screen.queryByTestId("composer-repo-link")).toBeNull();
  expect(screen.getByTestId("composer-repo-path").textContent).toBe("/repo");
});
