import "../session/testing/editorGeometry";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { LaunchOption } from "@evener/appwire-client";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { connectionStore } from "../../stores/connection";
import { readModuleCss } from "../../styles/cssBlock";
import promptCardStyles from "../../widgets/promptcard/promptcard.module.css";
import editorStyles from "../session/composer/skilleditor.module.css";
import Welcome from "../welcome/Welcome";
import { applySpawnURL, spawnDraftsStore } from "./spawnDrafts";
import {
  effortControl,
  expectWorkingDir,
  fillPrompt,
  installSpawnTestHooks,
  LAST_WORKING_DIR_KEY,
  modelListScript,
  modelState,
  modelTrigger,
  modelValue,
  readyClient,
  renderSpawn,
  settled,
  setupUser,
  setWorkingDir,
  workingDir,
} from "./spawnTestUtils";

// The card's effort control and the Advanced Options schema field share the
// wording "Reasoning effort": with the panel open, AT and label automation
// must still resolve each control unambiguously, so the card's own label
// carries its surface ("Prompt reasoning effort").
test("the card effort control keeps a distinct accessible name with Advanced options open", async () => {
  const user = setupUser();
  const advancedOption: LaunchOption = {
    field: "reasoning_effort",
    wireField: "reasoningEffort",
    label: "Reasoning effort",
    group: "model",
    kind: "select",
    perLaunch: true,
    choices: [
      { value: "low", label: "low" },
      { value: "high", label: "high" },
    ],
  };
  renderSpawn(
    readyClient((f) => {
      f.on("evener/launch/schema", () => ({ options: [advancedOption] }));
    }),
  );
  await settled();

  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  expect(screen.getByLabelText("Prompt reasoning effort")).toBe(effortControl());
  expect(screen.getAllByLabelText(/reasoning effort/i)).toHaveLength(2);
});
installSpawnTestHooks();

// --- the page's shape ------------------------------------------------------
//
// Prompt card first, taking the page's slack; ONE configuration row beneath it
// (working directory, model, effort); harness in Advanced options.

test("the desktop directory trigger announces the confirmed path", async () => {
  const user = setupUser();
  renderSpawn(readyClient());
  await settled();
  await setWorkingDir(user, "/tmp/project");
  expect(screen.getByLabelText("Working directory: /tmp/project", { selector: "#spawn-cwd" })).toBe(workingDir());
});

// Issue #198: the attach button, model trigger, and effort control are the
// composer's, in the composer's place - the card's own control row - rather
// than a fixed band at the foot of the viewport and bespoke rows in the
// settings list. jsdom renders the desktop and mobile layouts together (the
// split is CSS-only), so one render covers both. The mobile row order is the
// Treatment A list plus session-only Plugins. Model AND effort left it when
// the card took the job.
test("the directory and git info sit above the prompt; model and effort live in the card", async () => {
  renderSpawn(readyClient());
  await settled();

  const dir = screen.getByLabelText(/^Working directory:/, { selector: "#spawn-cwd" });
  const card = screen.getByTestId("spawn-prompt-card");
  const controls = screen.getByTestId("spawn-controls");
  expect(dir.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  // Below-card model/effort fields are gone: the desktop field wrapper and the
  // bordered Effort select no longer render.
  expect(screen.queryByTestId("spawn-desktop-model")).toBeNull();
  expect(screen.queryByLabelText("Effort")).toBeNull();
  // Model + effort are the card's own controls, beside attach and Start.
  expect(card.contains(controls)).toBe(true);
  expect(card.contains(screen.getByTestId("spawn-attach"))).toBe(true);
  expect(card.contains(modelTrigger())).toBe(true);
  expect(card.contains(effortControl())).toBe(true);
  expect(controls.querySelector("[data-testid='spawn-submit']")).toBeTruthy();
  // The prompt takes the page's vertical slack via its own min-height.
  expect(screen.getByRole("textbox", { name: "Prompt" }).style.minHeight).toBe("6lh");

  const mobileConfig = screen.getByTestId("spawn-mobile-config");
  expect(
    [...mobileConfig.querySelectorAll<HTMLElement>("[data-testid='mobile-spawn-row']")].map((row) => row.dataset.label),
  ).toEqual(["Harness", "Working directory", "Branch", "Access mode", "Plugins"]);
});

// The card's trigger says what the Model field says: "(default)" while the
// hub's own default will do, the chosen id once someone picks one.
test("the card's model trigger reads (default) until a model is picked, then names it", async () => {
  const user = setupUser();
  renderSpawn(readyClient());
  await settled();

  expect(modelState()).toBe("default");

  await user.click(modelTrigger());
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.clear(combo);
  await user.type(combo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));

  await waitFor(() => expect(modelValue().textContent).toBe("openai/gpt-5"));
});

// kata xgk8: a hub with no default to fall back on must not offer "(default)"
// anywhere, including on the card - the word reads exactly like Effort's own
// working default and invites a submit the daemon refuses.
test("the card's model trigger names the required choice when the hub has no default", async () => {
  const user = setupUser();
  renderSpawn(
    readyClient((f) => {
      f.on("evener/launch/resolve", () => ({ effective: { model: "" }, layers: {}, provenance: {} }));
      f.on("model/list", () => ({ data: [] }));
    }),
  );
  await settled();
  await setWorkingDir(user, "/tmp/project");

  await waitFor(() => expect(modelState()).toBe("required"));
});

test("mobile Spawn keeps the approved prompt hierarchy visible while the prompt is typed", async () => {
  const user = setupUser();
  renderSpawn(readyClient());
  await settled();

  await fillPrompt(user, "typed mobile work");

  expect(screen.getByTestId("pane-title-mobile").textContent).not.toBe("");
  const intro = screen.getByTestId("spawn-prompt-intro");
  expect(within(intro).getByRole("heading").textContent ?? "").not.toBe("");
  expect(intro.querySelector("p")?.textContent ?? "").not.toBe("");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe("typed mobile work");
});

// The placeholder repeated the heading and subtitle standing right above it
// almost word for word, so the field spent its one line saying what the page
// had already said. The dormant-start rule it also carried stays on the page,
// in that intro.
test("the prompt placeholder does not repeat the heading", async () => {
  renderSpawn(readyClient());
  await settled();

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement;
  const placeholder = prompt.dataset.placeholder ?? "";
  expect(placeholder).not.toBe("");
  // The intro above the card - heading and the dormant-start rule - says
  // something the placeholder does not repeat.
  const intro = screen.getByTestId("spawn-prompt-intro");
  expect(intro.querySelector("p")?.textContent ?? "").not.toBe("");
  for (const line of [...intro.children].map((child) => child.textContent ?? "")) {
    expect(line).not.toBe(placeholder);
  }
});

// The card's control-row compression and touch floors are pinned by the
// spawn-attach-in-card layoutguard case (geometric assertions against the
// real cascade at phone width: containment, active ellipsis on a long model
// id, the 44px effort tap floor), not by source-text matching here - a CSS
// grep passes even when the rules are unused or overridden.
test("mobile-only spawn hierarchy and row scale stay gated from desktop", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const spawnCss = readModuleCss(import.meta.url, "spawn.module.css");
  const rowsCss = readModuleCss(import.meta.url, "MobileSettingRows.module.css");
  const paneCss = readFileSync(join(here, "../../widgets/panescaffold/panescaffold.module.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    "",
  );

  // The prompt heading shows at every width now (critique R7); only the
  // settings-style rows stay phone-only.
  expect(spawnCss).toContain(".promptIntro");
  expect(spawnCss).not.toContain(".mobilePromptIntro");
  expect(spawnCss).toContain(".mobileConfig");
  expect(spawnCss).toContain("@media (max-width: 899px)");
  expect(rowsCss).toContain("min-height: 48px");
  expect(rowsCss).toContain("font-size: var(--font-size-body)");
  expect(paneCss).toContain(".desktopTitle");
  expect(paneCss).toContain(".mobileTitle");
  expect(paneCss).toContain("@media (max-width: 899px)");
});

// Harness moves into Advanced options: most installs have exactly one, so a
// field whose answer is always "evener" shouldn't lead the page. It stays fully
// functional there - the switch still blanks a non-evener model (see the harness
// tests in spawnHarnessModels.test.ts for that rule's own coverage).
test("harness moved into Advanced options, and still works there", async () => {
  const user = setupUser();
  renderSpawn(readyClient());
  await settled();

  expect(screen.queryByLabelText("Harness")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const harness = screen.getByLabelText("Harness") as HTMLSelectElement;
  await user.selectOptions(harness, "external");
  expect(harness.value).toBe("external");
});

// The approved mobile Treatment A action band uses the direct user-facing
// action "Start". The page title remains the existing React identity.
test("the primary verb is Start, in the card's own corner, and the page is titled to match", async () => {
  renderSpawn(readyClient());
  await settled();

  const start = screen.getByTestId("spawn-submit");
  // The verb is visible text, not an icon-only button named by aria-label.
  expect(start.textContent).toBe(start.getAttribute("aria-label"));
  expect(screen.getByTestId("pane-title-desktop").textContent).not.toBe("");
  // Inside the card, not in a detached actions strip below it.
  expect(screen.getByTestId("spawn-prompt-card").contains(start)).toBe(true);
  expect(screen.getByRole("button", { name: "Start" })).toBeTruthy();
});

// The word beside the paper plane collapses by PANE width, not viewport
// width: a docked pane squeezed narrow on a desktop display needs the same
// icon-only button the phone gets, and a viewport media query cannot see that
// (the overflowguard's 390px-pane-in-desktop-window measurement proved it).
// The 559px boundary matches the composer cluster's own compact threshold
// (the status row's first container query, statusrow.module.css).
test("the Start button's word collapses to the glyph below the compact pane threshold", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "spawn.module.css"), "utf8");
  expect(css).toMatch(/\.form\s*\{[^}]*container-type:\s*inline-size/);
  expect(css).toMatch(/@container \(max-width: 559px\)[\s\S]*?\.submitLabel\s*\{[^}]*display:\s*none/);
});

// Writing the prompt is what starting an agent IS, so the caret starts there
// rather than on whichever field happens to be first in the DOM.
test("the prompt field is focused on mount", async () => {
  renderSpawn(readyClient());
  await settled();
  expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Prompt" }));
});

// An unfocused spawn pane (a background tab) must never yank keyboard focus,
// and mobile must never pop the on-screen keyboard on load.
test("an unfocused pane never focuses the prompt field on mount", async () => {
  renderSpawn(readyClient(), false);
  await settled();
  expect(document.activeElement).not.toBe(screen.getByRole("textbox", { name: "Prompt" }));
});

test("a focused pane never focuses the prompt field on mount on mobile", async () => {
  vi.stubGlobal("matchMedia", ((query: string) => ({
    matches: true,
    media: query,
    addEventListener() {},
    removeEventListener() {},
  })) as unknown as typeof window.matchMedia);
  renderSpawn(readyClient());
  await settled();
  expect(document.activeElement).not.toBe(screen.getByRole("textbox", { name: "Prompt" }));
});

// The card and the session composer are the SAME object: both render
// widgets/promptcard. The class on the rendered card is the proof that reaches
// across both files (Composer.test.tsx asserts the mirror image).
test("the prompt card IS the shared PromptCard widget, not a lookalike", async () => {
  renderSpawn(readyClient());
  await settled();
  expect(screen.getByTestId("spawn-prompt-card").className.split(" ")).toContain(promptCardStyles.card);
});

// The card draws the one border and owns the focus ring, so the field inside
// must draw neither - otherwise it is a box inside a box. Caught in Chrome: the
// field rendered its own border inside the card's and its resize grabber floated
// loose in the corner between the two.
test("the prompt field is seamless, so the card's border is the only one", async () => {
  renderSpawn(readyClient());
  await settled();
  expect(screen.getByRole("textbox", { name: "Prompt" }).className.split(" ")).toContain(editorStyles.editor);
});

// --- branch: a read-only HEAD readout on the directory row -----------------

test("branch renders as a suffix on the directory row, not as an editable peer field", async () => {
  // The readout's only source is the chosen directory's HEAD, so a working
  // directory has to exist before there is a branch to show - an empty cwd
  // correctly renders nothing. ?dir= is how every other test here seeds one.
  window.history.pushState({}, "", "/new?dir=%2Fhome%2Fme%2Fapp");
  renderSpawn(readyClient());
  await settled();

  await waitFor(() => expect(screen.getByTestId("spawn-branch").textContent).toContain("main"));
  // Not a text box: it is a readout of the directory's HEAD.
  expect(screen.queryByLabelText("Branch")).toBeNull();
  expect(screen.getByTestId("spawn-branch").querySelector("input")).toBeNull();
});

test("the branch readout is absent when the working directory has no resolvable HEAD", async () => {
  localStorage.setItem("evener-hub.spawn-defaults.global.working_dir", "/tmp/plain");
  renderSpawn(
    readyClient((fake) => {
      fake.on("evener/git/head", () => {
        throw new Error("git head unavailable");
      });
    }),
  );
  await settled();

  await waitFor(() => expectWorkingDir("/tmp/plain"));
  expect(screen.queryByTestId("spawn-branch")).toBeNull();
});

// The icon controls draw real SVG glyphs rather than bare "+"/"×" characters,
// matching the composer's own attach control. Their spoken names come from
// IconButton's label either way.
test("the attach control draws an SVG glyph, not a literal text character", async () => {
  renderSpawn(readyClient());
  await settled();

  const attach = screen.getByRole("button", { name: "Attach image" });
  expect(attach.querySelector("svg")).toBeTruthy();
  expect(attach.textContent).toBe("");
});

test("Access mode moved from the top-level bar into Advanced options (9ct0)", async () => {
  const user = setupUser();
  renderSpawn(readyClient());
  await settled();

  expect(screen.queryByLabelText("Access mode")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  expect(screen.getByLabelText("Access mode")).toBeTruthy();
});

test("a full submit sends the cwd, prompt, and access-mode sandbox, then routes to /s/{ref}", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "do the thing");
  await setWorkingDir(user, "/tmp/project");
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Access mode"), "Read-only");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({
    cwd: "/tmp/project",
    input: [{ type: "text", text: "do the thing" }],
    launchOverrides: { sandbox: "read-only" },
  });
  // Sticky defaults persist the working dir globally on submit (floor §1.9).
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.working_dir")).toBe("/tmp/project");
});

// A blank prompt starts a DORMANT session, exactly as the placeholder
// promises. The daemon honours it: hubThreadStart calls StartTurn only when
// len(params.Input) > 0 (cmd/evener-hub/app_threadlifecycle.go), and buildInput
// drops a blank prompt, so the wire carries input: [] - the session is created
// and no turn is started.
test("an empty prompt starts a dormant session rather than erroring", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ cwd: "/tmp/project", input: [] });
  expect(screen.queryByText(/prompt is empty/i)).toBeNull();
});

// Whitespace is a blank prompt: buildInput keeps the text item only when it is
// non-empty AFTER trimming, so "   " takes the same dormant path rather than
// starting a turn that says nothing.
test("a whitespace-only prompt starts a dormant session too", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "   ");
  await setWorkingDir(user, "/tmp/project");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ input: [] });
});

test("loads sticky defaults from localStorage on mount", async () => {
  const user = setupUser();
  localStorage.setItem("evener-hub.spawn-defaults.global.working_dir", "/saved/project");
  localStorage.setItem("evener-hub.spawn-defaults./saved/project", JSON.stringify({ access_mode: "workspace-write" }));
  renderSpawn(readyClient());
  await settled();

  await waitFor(() => expectWorkingDir("/saved/project"));
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  expect((screen.getByLabelText("Access mode") as HTMLSelectElement).value).toBe("workspace-write");
});

test("Welcome preserves URL-prefilled setup fields when routing to Spawn", async () => {
  window.history.pushState({}, "", "/?dir=%2Fhome%2Fme%2Fapp&prompt=fix%20it#setup");
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  });
  connectionStore.getState().connect(client);
  const welcome = render(<Welcome params={{}} paneId="welcome" focused={true} />);
  await waitFor(() => expect(window.location.pathname).toBe("/new"));
  welcome.unmount();
  renderSpawn(client);
  await settled();
  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe("fix it"),
  );
  expectWorkingDir("/home/me/app");
  expect(window.location.hash).toBe("#setup");
});

test("prefills the prompt and working dir from ?dir=/?prompt=", async () => {
  window.history.pushState({}, "", "/new?dir=%2Fhome%2Fme%2Fapp&prompt=fix%20it");
  renderSpawn(readyClient());
  await settled();

  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe("fix it"),
  );
  expectWorkingDir("/home/me/app");
});

// kata 11ee: the spawn pane is a dockview singleton (index.tsx), so a second
// /new?dir=<encoded> navigation while it's already open (and mounted) never
// remounts it - the singleton refocus just updates workspace.ts's
// focusedPaneId, it doesn't tear down and recreate Spawn's own React tree.
// The mount-only prefill effect (readUrlPrefill in a []-deps useEffect) then
// never reruns, so the second dir prefill is silently dropped. Reproduced
// here at the level Spawn.tsx itself can observe it: window.location.search
// changes and the SAME instance receives the routing.ts navigate() signal
// (pushState + a synthetic "popstate", exactly as AppShell's own listener
// and project.tsx's useQueryCwd precedent both key off) with no unmount in
// between.
test("kata 11ee: a second ?dir= navigation while already mounted still prefills the working dir", async () => {
  window.history.pushState({}, "", "/new?dir=%2Fhome%2Fme%2Fapp");
  renderSpawn(readyClient());
  await settled();
  await waitFor(() => expectWorkingDir("/home/me/app"));

  act(() => {
    window.history.pushState({}, "", "/new?dir=%2Fhome%2Fother");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  await waitFor(() => expectWorkingDir("/home/other"));
  await settled();
});

// Same defect class as the ?dir= case above - readUrlPrefill's ?prompt= entry
// goes through the identical mount-only effect, so a repeat "Spawn with
// prompt" palette command (shell/palette/commands.ts's own /new?prompt= nav)
// while the pane is already open must refill the prompt too.
test("kata 11ee: a second ?prompt= navigation while already mounted still prefills the prompt", async () => {
  window.history.pushState({}, "", "/new?prompt=first");
  renderSpawn(readyClient(), true, { connectStore: false });
  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe("first"),
  );

  act(() => {
    window.history.pushState({}, "", "/new?prompt=second");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe("second"),
  );
});

// Guards against a naive fix that unconditionally re-applies BOTH fields on
// every popstate: a navigation that carries neither param (e.g. some other
// in-app nav, then back to a plain /new) must never clobber values already
// typed into the form - readUrlPrefill's own "absent param -> no entry"
// contract (urlPrefill.test.ts) has to keep holding on every later
// navigation, not just the first mount.
test("kata 11ee: a navigation with no ?dir=/?prompt= at all leaves already-typed values untouched", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=%2Fhome%2Fme%2Fapp");
  renderSpawn(readyClient(), true, { connectStore: false });
  await waitFor(() => expectWorkingDir("/home/me/app"));
  await fillPrompt(user, "typed by hand");

  act(() => {
    window.history.pushState({}, "", "/new");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  expectWorkingDir("/home/me/app");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe("typed by hand");
});

test("only confirming a directory updates the launch defaults", async () => {
  const user = setupUser();
  renderSpawn(readyClient((f) => f.on("evener/paths/complete", () => ({ data: ["/tmp/project/src"] }))));
  await settled();
  await user.click(workingDir());
  await user.click(await screen.findByRole("button", { name: "Open /tmp/project/src" }));
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBeNull();
  const confirm = screen.getByRole("button", { name: "Use this folder" });
  await waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false));
  await user.click(confirm);
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBe("/tmp/project/src");
});

test("Escape discards directory browsing while preserving the prompt and launch directory", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=%2Ftmp%2Fproject");
  const fake = readyClient((f) => f.on("evener/paths/complete", () => ({ data: ["/tmp/project/src"] })));
  renderSpawn(fake);
  await settled();
  await fillPrompt(user, "my important draft text");
  await user.click(workingDir());
  await user.click(await screen.findByRole("button", { name: "Open /tmp/project/src" }));
  const pathname = window.location.pathname;
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "Choose directory" })).toBeNull();
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(window.location.pathname).toBe(pathname);
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe(
    "my important draft text",
  );
  expectWorkingDir("/tmp/project");
});

// The read side of that same global (spec 3.4): with no ?dir= prefill and no
// per-project blob the field is empty, and the panel opens on the last
// directory a session was launched in rather than on $HOME.
test("the browse panel opens on the stamped last-working-directory global", async () => {
  const user = setupUser();
  localStorage.setItem(LAST_WORKING_DIR_KEY, "/home/me/lastone");
  const complete = vi.fn((_params: { prefix: string }) => ({ data: ["/home/me/lastone/src"] }));
  renderSpawn(readyClient((f) => f.on("evener/paths/complete", complete)));
  await settled();
  // Nothing else may have seeded the field: the fallback is only consulted
  // when the value is empty, and an empty field shows its placeholder.
  expect(workingDir().dataset.state).toBe("unset");

  await user.click(workingDir());
  await screen.findByRole("textbox", { name: "Path" });

  await waitFor(() => expect(complete.mock.calls.map(([params]) => params.prefix)).toContain("/home/me/lastone/"));
});

// Both list RPCs behind the working-directory field return a Go slice, which
// marshals as JSON null rather than [] when it is empty - a hub with no
// remembered projects, or a directory with no children, answers `null`. Caught
// against a real hub: the panel crashed on mount reading .length of null.
test("survives a null data payload from either list RPC", async () => {
  const user = setupUser();
  const nulled = { data: null as unknown as string[] };
  renderSpawn(
    readyClient((f) => {
      f.on("evener/projects/recent", () => nulled);
      f.on("evener/paths/complete", () => nulled);
    }),
  );
  await settled();

  await user.click(workingDir());

  // The panel is up, listing nothing, rather than having thrown its tree away.
  expect(await screen.findByRole("textbox", { name: "Path" })).toBeTruthy();
  await waitFor(() => expect(document.querySelector('[role="status"][data-state="empty"]')).not.toBeNull());
});

test("offers to create a missing directory, then creates it and spawns", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/path/validate", () => ({
      path: "/tmp/new",
      valid: false,
      error: "stat /tmp/new: no such file or directory",
    }));
    f.on("evener/dirs/create", () => ({ path: "/tmp/new", created: true }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/new");
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "go");
  await user.click(screen.getByTestId("spawn-submit"));

  await user.click(await screen.findByRole("button", { name: "Create & start" }));

  await waitFor(() =>
    expect(fake.calls).toContainEqual({ method: "evener/dirs/create", params: { path: "/tmp/new" } }),
  );
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
  // doSpawn's busy reset is shared by both callers - handleCreateConfirm's
  // success path re-enables the button the same way handleSpawn's does.
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
});

test("aborts with the validator message for a non-fixable working dir, then a corrected retry actually spawns", async () => {
  const user = setupUser();
  let dirIsValid = false;
  const fake = readyClient((f) => {
    f.on("evener/path/validate", () =>
      dirIsValid
        ? { path: "/tmp/project", valid: true }
        : { path: "/etc/hosts", valid: false, error: "path is not a directory" },
    );
  });
  window.history.pushState({}, "", "/new?dir=/etc/hosts");
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "go");
  await user.click(screen.getByTestId("spawn-submit"));

  expect(await screen.findByText("path is not a directory")).toBeTruthy();
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  // Failure paths already reset busy (verified, unchanged by this fix) - the
  // button must stay usable so the user can correct the path and retry.
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);

  // kata 61v2 corollary: the VISUAL state resetting is not proof the guard of
  // record (busyRef) released too - only an actual second spawn proves that.
  dirIsValid = true;
  await setWorkingDir(user, "/tmp/project");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "thread/start")).toHaveLength(1));
});

// kata xkp2: filed as "the Spawn button is enabled but a click with the
// working directory left at its placeholder does nothing - no session, no
// toast, no dialog, no request reaches the daemon". Investigated with a
// evener/path/validate response that mirrors the real daemon EXACTLY for an
// empty path: fspaths.ValidateLaunchPath rejects an empty (or all-
// whitespace) path unconditionally, before it even looks at `kind`
// (cmd/evener-hub/internal/fspaths/app_paths.go:150-154), with the literal
// string "path is required" - one of preflightDir's own NON_FIXABLE_REASONS.
// Given that real response, the click above aborts exactly like the
// non-fixable case one test up: visibly, via a toast, with zero thread/start
// calls. No silent path was found (see the kata comment for the full
// writeup) - this is coverage for a state nothing exercised before: every
// other spawn test either sets a working directory first, or leans on
// readyClient()'s validate stub, which (unlike the real daemon) answers
// "valid" for any path including "".
test("kata xkp2: Spawn with the working directory left at its placeholder aborts visibly, not silently", async () => {
  // This test's pathname assertion below names the suite's original default
  // route literally, so the test keeps that starting state explicitly; a
  // validation abort never reaches navigation on any route.
  window.history.pushState({}, "", "/");
  const user = setupUser();
  const fake = readyClient((f) => {
    // path: "" is the real wire shape too - ValidateLaunchPath's early
    // return leaves the Go struct's Path field at its zero value.
    f.on("evener/path/validate", () => ({ path: "", valid: false, error: "path is required" }));
  });
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "say hello");
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  // The state the kata's DOM readout recorded: enabled, no aria-disabled,
  // the working-directory control still showing its placeholder.
  expect(button.disabled).toBe(false);
  expect(button.getAttribute("aria-disabled")).toBeNull();
  expect(workingDir().dataset.state).toBe("unset");

  await user.click(button);

  expect(await screen.findByText("path is required")).toBeTruthy();
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(window.location.pathname).toBe("/");
  // Released, not stuck disabled/"Spawning…" (kata 61v2's own failure class).
  expect(button.disabled).toBe(false);
});

test("a ?host= prefill seeds the draft's launch host (the rail's project-copy spawn carries it)", () => {
  window.history.replaceState({}, "", "/new?dir=/repo/x&host=devbox");
  applySpawnURL();
  const draft = spawnDraftsStore.getState().current;
  expect(draft?.fields.getState().source).toBe("devbox");
  window.history.replaceState({}, "", "/");
});

test("a ?host=local prefill overrides the draft's last-chosen host for the same directory", () => {
  window.history.replaceState({}, "", "/new?dir=/repo/x&host=devbox");
  applySpawnURL();
  expect(spawnDraftsStore.getState().current?.fields.getState().source).toBe("devbox");
  // The same project's local copy launches with the same cwd; the prefill
  // must retarget the draft, not leave the remote host sticky.
  window.history.replaceState({}, "", "/new?dir=/repo/x&host=local");
  applySpawnURL();
  expect(spawnDraftsStore.getState().current?.fields.getState().source).toBe("local");
  window.history.replaceState({}, "", "/");
});

test("an open launch picker adopts a provider-file repair notification", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();
  await user.click(modelTrigger());
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.clear(combo);
  await user.type(combo, "repaired-model");
  expect(screen.queryByText("work/repaired-model")).toBeNull();
  modelListScript.override = [
    { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
    { provider: "work", model: "repaired-model", displayName: "work/repaired-model" },
  ];
  act(() => fake.emitNotification({ method: "evener/auth/updated", params: {} }));
  await screen.findByText("work/repaired-model");
});
