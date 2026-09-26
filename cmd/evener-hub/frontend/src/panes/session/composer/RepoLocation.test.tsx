import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { FakeClient, gateSettlements } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { ClientProvider } from "../../../shell/clientContext";
import { RepoLocation } from "./RepoLocation";

afterEach(cleanup);

function locationCss(): string {
  const cssPath = join(dirname(fileURLToPath(import.meta.url)), "repoLocation.module.css");
  // Comments stripped: the rules' own comments name the classes (testing.md's
  // "a stylesheet assertion that matches its own comment" trap).
  return readFileSync(cssPath, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

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
  // The anchor, not the span inside it, owns the tooltip: a title on the span
  // would shadow this one for any hover over the visible text, leaving only a
  // restatement of what is already on screen.
  expect(link.getAttribute("title")).toBe("Open owner/repo#feature/x on GitHub");
  expect(screen.getByTestId("composer-repo-ref").getAttribute("title")).toBeNull();
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

// A lookup attempted while the hub is away fails soft and is cached empty.
// Neither `client` nor `cwd` changes when the SAME client reconnects, so only a
// connection-state transition can tell the composer the lookup is worth
// retrying (issue #1355).
test("retries a lookup attempted while the hub was reconnecting, once it is ready", async () => {
  const client = new FakeClient("reconnecting");
  client.on("evener/git/head", () => ({ head: "main", originUrl: "git@github.com:owner/repo.git" }));
  renderLocation("/repo", client);

  // The not-ready request was rejected before it was recorded, so the cwd
  // stands alone with no branch and no link.
  expect(screen.queryByTestId("composer-repo-ref")).toBeNull();

  client.emitStateChange("ready");

  expect((await screen.findByTestId("composer-repo-ref")).textContent).toBe("owner/repo#main");
  expect(client.calls).toHaveLength(1);
});

// The same recovery must re-run a lookup that failed on the wire while the
// connection was nominally ready (a request that died as the socket dropped).
test("re-runs a failed lookup when the connection recovers", async () => {
  const client = new FakeClient();
  let attempt = 0;
  client.on("evener/git/head", () => {
    attempt += 1;
    if (attempt === 1) throw new Error("hub unavailable");
    return { head: "feature/x", originUrl: "git@github.com:owner/repo.git" };
  });
  renderLocation("/repo", client);

  await waitFor(() => expect(client.calls).toHaveLength(1));
  expect(screen.queryByTestId("composer-repo-ref")).toBeNull();

  client.emitStateChange("reconnecting");
  client.emitStateChange("ready");

  expect((await screen.findByTestId("composer-repo-ref")).textContent).toBe("owner/repo#feature/x");
  expect(client.calls).toHaveLength(2);
});

// A retry can start while the pre-drop lookup is still in flight; a late answer
// from before the recovery must not overwrite the retry's fresher result.
test("a stale pre-recovery response cannot overwrite the retry's result", async () => {
  const client = new FakeClient();
  const answers = gateSettlements(client, "evener/git/head");
  renderLocation("/repo", client);
  await waitFor(() => expect(answers).toHaveLength(1));

  client.emitStateChange("reconnecting");
  client.emitStateChange("ready");
  await waitFor(() => expect(answers).toHaveLength(2));

  answers[1]?.resolve({ head: "fresh", originUrl: "" });
  expect((await screen.findByTestId("composer-repo-branch")).textContent).toBe("fresh");

  answers[0]?.resolve({ head: "stale", originUrl: "" });
  await waitFor(() => expect(screen.getByTestId("composer-repo-branch").textContent).toBe("fresh"));
});

// A recovered lookup that still finds no repository leaves the line on the cwd
// alone: recovery must not invent a branch.
test("stays on the cwd alone when a recovered lookup finds no repo", async () => {
  const client = clientReporting({ head: "" });
  renderLocation("/tmp/plain", client);

  await waitFor(() => expect(client.calls).toHaveLength(1));
  client.emitStateChange("reconnecting");
  client.emitStateChange("ready");
  await waitFor(() => expect(client.calls).toHaveLength(2));

  expect(screen.getByTestId("composer-repo-path").textContent).toBe("/tmp/plain");
  expect(screen.queryByTestId("composer-repo-branch")).toBeNull();
  expect(screen.queryByTestId("composer-repo-link")).toBeNull();
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

// --- Middle truncation ------------------------------------------------------
//
// The working dir and the repo reference middle-truncate under pressure, not
// end-truncate: an end-truncation hid the line's endings - the project
// directory, the branch - which are exactly the parts a person scanning the
// line needs (the same call ToolRow.tsx's collapsed summary already made).
// The split is DOM the CSS acts on: the head span ellipsis-clamps under
// pressure while the tail never shrinks, so the ending stays on screen.

test("a long working dir renders head-then-tail spans that concatenate to the path", () => {
  const client = new FakeClient();
  const cwd = "/home/jesse/git/prime-radiant-inc/evener";
  renderLocation(cwd, client, false);

  const path = screen.getByTestId("composer-repo-path");
  expect(path.children).toHaveLength(2);
  const head = path.children[0]?.textContent ?? "";
  const tail = path.children[1]?.textContent ?? "";
  expect(head + tail).toBe(cwd);
  // The tail is what an end-truncation used to hide: the path's ending.
  expect(tail.endsWith("evener")).toBe(true);
  expect(head.startsWith("/home/jesse")).toBe(true);
});

test("the repo reference splits at its branch marker so the branch rides the never-shrinking tail", async () => {
  const client = clientReporting({ head: "feature/x", originUrl: "git@github.com:owner/repo.git" });
  renderLocation("/repo", client);

  const ref = await screen.findByTestId("composer-repo-ref");
  expect(ref.children).toHaveLength(2);
  expect(ref.children[0]?.textContent).toBe("owner/repo");
  expect(ref.children[1]?.textContent).toBe("#feature/x");
});

// The cut walks off boundary whitespace before splitting: head and tail render
// as separate spans, and CSS white-space processing drops a space at a span
// edge, so every space must stay interior to one of the two.
test("a cut beside whitespace moves off it, keeping the space interior to one span", () => {
  const client = new FakeClient();
  // ceil(8 × 0.6) = 5 lands directly after the string's only space.
  renderLocation("aaaa bbb", client, false);

  const path = screen.getByTestId("composer-repo-path");
  const head = path.children[0]?.textContent ?? "";
  const tail = path.children[1]?.textContent ?? "";
  expect(head).toBe("aaa");
  expect(tail).toBe("a bbb");
  expect(head + tail).toBe("aaaa bbb");
});

// A string with no room for both sides is not middle-truncated: it renders as
// one head span, which under pressure end-truncates exactly as the whole line
// did before the split existed.
test("a string too short to split renders as a single span", () => {
  const client = new FakeClient();
  renderLocation("/", client, false);

  const path = screen.getByTestId("composer-repo-path");
  expect(path.children).toHaveLength(1);
  expect(path.textContent).toBe("/");
});

// The pressure grammar itself: the path and the reference lay their spans out
// as flex rows, and the pair is COMPOSED from the grammar's home
// (toolcallitem.module.css's .clampedHead/.clampedTail) rather than copied, so
// the two implementations cannot drift apart - toolRowGrammar.test.tsx pins
// the declarations where they live.
test("the head and tail spans carry the middle-truncation grammar", () => {
  const css = locationCss();
  expect(css).toMatch(/\.path \{[^}]*display: flex;/);
  expect(css).toMatch(/\.ref \{[^}]*display: flex;/);
  expect(css).toMatch(/\.head \{[^}]*composes: clampedHead from "\.\.\/transcript\/toolcallitem\.module\.css";/);
  expect(css).toMatch(/\.tail \{[^}]*composes: clampedTail from "\.\.\/transcript\/toolcallitem\.module\.css";/);
});

// On the phone the footer's bottom padding is the home-indicator band
// (PaneScaffold publishes it as --pane-footer-pad-bottom). The line spends up
// to one of its own line-heights of that band beyond its breathing room
// (--space-3), so the strip under it stops reading as dead padding - and
// exactly zero once the band is down to that breathing room, so the keyboard
// open or a desktop window squeezed to phone width (env() is 0, band is
// plain --space-3) drops nothing at all. Desktop is untouched.
test("the line translates down into the footer's padding band on the phone only", () => {
  const css = locationCss();
  expect(css).toMatch(
    /@media \(max-width: 899px\) \{[\s\S]*?\.line \{[^}]*transform: translateY\(\s*clamp\(\s*0px,\s*calc\(var\(--pane-footer-pad-bottom, 0px\) - var\(--space-3\)\),\s*calc\(var\(--font-size-caption\) \* var\(--line-height-body\)\)/,
  );
  const baseLine = css.match(/^\.line \{([^}]*)\}/m)?.[1] ?? "";
  expect(baseLine).not.toContain("transform");
});
