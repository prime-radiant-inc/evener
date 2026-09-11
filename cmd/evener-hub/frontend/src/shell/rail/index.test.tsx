import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Component, type ReactNode } from "react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { RailHost, resetRailChunkForTests } from "./index";
import * as railHostChunk from "./railHostChunk";
import { resetRailHostLoaderForTests } from "./railHostChunk";

// The RailHost chunk is a separate network request from index.html, so a hub
// restarting mid-load, a slow link, or a deploy that replaced the hashed
// filename all land on a rejected import() - the browser's own "Failed to
// fetch dynamically imported module". Replacing the loader is that failure
// with no network involved; the real 1605-line Rail tree never loads here.
//
// A hoisted vi.mock("./railHostChunk", ...) here would swap the module in the
// shared registry - under isolate:false that registry is shared by every file
// in the worker, and whichever file happens to instantiate index.tsx FIRST in
// the whole worker's lifetime permanently fixes its closure over loadRailHost
// to whatever was in effect at that moment (DockRegion.test.tsx's own comment
// on the same hazard). vi.spyOn on the namespace import below instead MUTATES
// the shared railHostChunk module object's own `loadRailHost` property in
// place - Vite's module-runner gives named imports a live getter into that
// same object, so index.tsx's calls see the spy's current implementation
// regardless of when it was instantiated, and mockRestore() in afterEach
// cleanly hands the real function back for whatever file runs next.
const realLoadRailHost = railHostChunk.loadRailHost;
const loadRailHost = vi.spyOn(railHostChunk, "loadRailHost");

const CHUNK_ERROR = "Failed to fetch dynamically imported module: /webassets/RailHost-a1b2c3.js";

function StubRailHost() {
  return <p>rail host mounted</p>;
}

// Suppress console.error noise from React's error-boundary logging during
// tests that deliberately trigger chunk-load failures. The errors are
// expected; the boundary catches them. Matched on React's own stable
// componentDidCatch format string plus its fixed boundary-recovery
// sentence, rather than blanket-silenced, so any *other* console.error a
// regression here might produce still reaches real console.error and
// stays visible in test output (DockRegion.test.tsx's own recipe).
const REACT_ERROR_BOUNDARY_FORMAT = "%o\n\n%s\n\n%s\n";
const REACT_ERROR_BOUNDARY_PREFACE = "The above error occurred in one of your React components.";
const realConsoleError = console.error.bind(console);
let consoleErrorSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  consoleErrorSpy = vi.spyOn(console, "error").mockImplementation((...args: unknown[]) => {
    if (args[0] === REACT_ERROR_BOUNDARY_FORMAT && args[2] === REACT_ERROR_BOUNDARY_PREFACE) {
      return;
    }
    realConsoleError(...args);
  });
  loadRailHost.mockReset();
  loadRailHost.mockImplementation(realLoadRailHost);
  // The chunk is one shared lazy() payload per page load, so each test needs
  // its own - a payload that resolved (or rejected) in the last test would
  // never call this test's loader at all.
  resetRailChunkForTests();
  resetRailHostLoaderForTests();
});

afterEach(() => {
  consoleErrorSpy.mockRestore();
  cleanup();
  // Whichever override the LAST test set (mockRejectedValue/mockResolvedValue/
  // mockResolvedValueOnce...) would otherwise still be armed on this shared
  // spy for the next file in the worker that calls the real loadRailHost -
  // see this file's own comment on the vi.spyOn call above.
  loadRailHost.mockReset();
  loadRailHost.mockImplementation(realLoadRailHost);
  vi.unstubAllGlobals();
});

test("a rejected RailHost chunk shows the failure message with a Retry", async () => {
  vi.mocked(loadRailHost).mockRejectedValue(new Error(CHUNK_ERROR));

  render(<RailHost />);

  expect(await screen.findByText("Couldn't load the sidebar")).toBeTruthy();
  expect(screen.getByText(CHUNK_ERROR)).toBeTruthy();
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
});

test("the failure stands in a rail-sized shell holding the row's width", async () => {
  vi.mocked(loadRailHost).mockRejectedValue(new Error(CHUNK_ERROR));

  render(<RailHost />);

  const failure = await screen.findByTestId("rail-chunk-failure");
  // The same flex:none + width + background + right-border the live rail
  // draws (Rail.module.css's .rail), so the desktop content row keeps its
  // width and styling and the workspace beside it never stretches.
  expect(failure.className).toMatch(/rail/);
  const style = getComputedStyle(failure);
  expect(style.getPropertyValue("--rail-width").trim()).not.toBe("");
});

test("Retry fetches the chunk again and mounts the host on the second attempt", async () => {
  vi.mocked(loadRailHost)
    .mockRejectedValueOnce(new Error(CHUNK_ERROR))
    .mockResolvedValueOnce({ RailHost: StubRailHost });
  const user = userEvent.setup();

  render(<RailHost />);
  await screen.findByText("Couldn't load the sidebar");
  await user.click(screen.getByRole("button", { name: "Retry" }));

  // Both halves of a retry, in one assertion each: the host it returns
  // replaces the failure state, and the second attempt asks the loader for
  // the cache-busted path. A second same-URL import does not reach Chrome's
  // network stack - it replays the cached failure.
  expect(await screen.findByText("rail host mounted")).toBeTruthy();
  expect(vi.mocked(loadRailHost).mock.calls).toEqual([[false], [true]]);
  expect(screen.queryByText("Couldn't load the sidebar")).toBeNull();
});

test("a successful retry is reused after the wrapper unmounts and remounts", async () => {
  vi.mocked(loadRailHost)
    .mockRejectedValueOnce(new Error(CHUNK_ERROR))
    .mockResolvedValueOnce({ RailHost: StubRailHost });
  const user = userEvent.setup();

  const first = render(<RailHost />);
  await screen.findByText("Couldn't load the sidebar");
  await user.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("rail host mounted")).toBeTruthy();

  first.unmount();
  render(<RailHost />);

  expect(await screen.findByText("rail host mounted")).toBeTruthy();
  expect(vi.mocked(loadRailHost).mock.calls).toEqual([[false], [true]]);
});

test("a retry that fails again offers a page reload instead of stranding the sidebar", async () => {
  vi.mocked(loadRailHost).mockRejectedValue(new Error(CHUNK_ERROR));
  const reload = vi.fn();
  vi.stubGlobal("location", { ...window.location, reload });
  const user = userEvent.setup();

  render(<RailHost />);
  await screen.findByText("Couldn't load the sidebar");
  // The first failure offers only the cache-busted retry: a deploy that
  // removed the hashed chunk is still only one hypothesis among transient
  // ones, so the reload is the second-strike path, not the first.
  expect(screen.queryByRole("button", { name: "Reload page" })).toBeNull();

  await user.click(screen.getByRole("button", { name: "Retry" }));
  await user.click(await screen.findByRole("button", { name: "Reload page" }));

  expect(reload).toHaveBeenCalledTimes(1);
  expect(vi.mocked(loadRailHost).mock.calls).toEqual([[false], [true]]);
});

test("an ordinary retry failure does not prescribe a page reload", async () => {
  // A chunk fetch that fails WITHOUT naming a stale hashed asset (a 500
  // page's HTML where the chunk bytes should be, a proxy error page) is not
  // a chunk-load failure: the rail boundary declines it, so it lands on the
  // next boundary above - never the rail failure state, and so never the
  // Retry/Reload pair that could misreport it as a stale deploy.
  vi.mocked(loadRailHost).mockRejectedValue(new Error("RailHost chunk request failed with status 500"));

  render(
    <RailTestOuterBoundary>
      <RailHost />
    </RailTestOuterBoundary>,
  );

  expect(await screen.findByText("outer boundary caught: RailHost chunk request failed with status 500")).toBeTruthy();
  expect(screen.queryByText("Couldn't load the sidebar")).toBeNull();
  expect(screen.queryByRole("button", { name: "Reload page" })).toBeNull();
});

// A logic bug thrown by the RESOLVED chunk's own render is not a chunk-load
// failure: the rail boundary must let it keep unwinding to the next boundary
// above instead of misreporting it as a failed fetch with a Retry.
class RailTestOuterBoundary extends Component<{ children: ReactNode }, { failure: string | null }> {
  state = { failure: null as string | null };
  static getDerivedStateFromError(error: unknown) {
    return { failure: error instanceof Error ? error.message : String(error) };
  }
  render(): ReactNode {
    if (this.state.failure !== null) return <p>outer boundary caught: {this.state.failure}</p>;
    return this.props.children;
  }
}

test("a logic bug in the resolved chunk keeps unwinding past the rail boundary", async () => {
  const error = new Error("RailHost render logic bug");
  const onCaughtError = vi.fn();
  vi.mocked(loadRailHost).mockResolvedValue({
    RailHost: () => {
      throw error;
    },
  });

  render(
    <RailTestOuterBoundary>
      <RailHost />
    </RailTestOuterBoundary>,
    { onCaughtError },
  );

  expect(await screen.findByText("outer boundary caught: RailHost render logic bug")).toBeTruthy();
  expect(screen.queryByText("Couldn't load the sidebar")).toBeNull();
  // Capture React's expected diagnostic at this root, not via a broader
  // console filter. Any additional or different caught error fails here.
  expect(onCaughtError).toHaveBeenCalledTimes(1);
  expect(onCaughtError.mock.calls[0]?.[0]).toBe(error);
  expect(onCaughtError.mock.calls[0]?.[1].errorBoundary).toBeInstanceOf(RailTestOuterBoundary);
});
