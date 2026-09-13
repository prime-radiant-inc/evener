import { cleanup, type RenderOptions, render, screen } from "@testing-library/react";
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

const realConsoleError = console.error.bind(console);

// Capture only this render's injected failure; every test also asserts the
// complete callback count and error identity, including repeated retries.
function captureExpectedError(expectedError: Error) {
  return vi.fn<NonNullable<RenderOptions["onCaughtError"]>>((error, info) => {
    if (error !== expectedError) realConsoleError(error, info);
  });
}

beforeEach(() => {
  loadRailHost.mockReset();
  loadRailHost.mockImplementation(realLoadRailHost);
  // The chunk is one shared lazy() payload per page load, so each test needs
  // its own - a payload that resolved (or rejected) in the last test would
  // never call this test's loader at all.
  resetRailChunkForTests();
  resetRailHostLoaderForTests();
});

afterEach(() => {
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
  const chunkError = new Error(CHUNK_ERROR);
  const onCaughtError = captureExpectedError(chunkError);
  vi.mocked(loadRailHost).mockRejectedValue(chunkError);

  render(<RailHost />, { onCaughtError });

  expect(await screen.findByText("Couldn't load the sidebar")).toBeTruthy();
  expect(screen.getByText(CHUNK_ERROR)).toBeTruthy();
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  expect(onCaughtError).toHaveBeenCalledTimes(1);
  expect(onCaughtError.mock.calls[0]?.[0]).toBe(chunkError);
});

test("the failure stands in a rail-sized shell holding the row's width", async () => {
  const chunkError = new Error(CHUNK_ERROR);
  const onCaughtError = captureExpectedError(chunkError);
  vi.mocked(loadRailHost).mockRejectedValue(chunkError);

  render(<RailHost />, { onCaughtError });

  const failure = await screen.findByTestId("rail-chunk-failure");
  // The same flex:none + width + background + right-border the live rail
  // draws (Rail.module.css's .rail), so the desktop content row keeps its
  // width and styling and the workspace beside it never stretches.
  expect(failure.className).toMatch(/rail/);
  const style = getComputedStyle(failure);
  expect(style.getPropertyValue("--rail-width").trim()).not.toBe("");
  expect(onCaughtError).toHaveBeenCalledTimes(1);
  expect(onCaughtError.mock.calls[0]?.[0]).toBe(chunkError);
});

test("Retry fetches the chunk again and mounts the host on the second attempt", async () => {
  const chunkError = new Error(CHUNK_ERROR);
  const onCaughtError = captureExpectedError(chunkError);
  vi.mocked(loadRailHost).mockRejectedValueOnce(chunkError).mockResolvedValueOnce({ RailHost: StubRailHost });
  const user = userEvent.setup();

  render(<RailHost />, { onCaughtError });
  await screen.findByText("Couldn't load the sidebar");
  await user.click(screen.getByRole("button", { name: "Retry" }));

  // Both halves of a retry, in one assertion each: the host it returns
  // replaces the failure state, and the second attempt asks the loader for
  // the cache-busted path. A second same-URL import does not reach Chrome's
  // network stack - it replays the cached failure.
  expect(await screen.findByText("rail host mounted")).toBeTruthy();
  expect(vi.mocked(loadRailHost).mock.calls).toEqual([[false], [true]]);
  expect(screen.queryByText("Couldn't load the sidebar")).toBeNull();
  expect(onCaughtError).toHaveBeenCalledTimes(1);
  expect(onCaughtError.mock.calls[0]?.[0]).toBe(chunkError);
});

test("a successful retry is reused after the wrapper unmounts and remounts", async () => {
  const chunkError = new Error(CHUNK_ERROR);
  const onCaughtError = captureExpectedError(chunkError);
  vi.mocked(loadRailHost).mockRejectedValueOnce(chunkError).mockResolvedValueOnce({ RailHost: StubRailHost });
  const user = userEvent.setup();

  const first = render(<RailHost />, { onCaughtError });
  await screen.findByText("Couldn't load the sidebar");
  await user.click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("rail host mounted")).toBeTruthy();

  first.unmount();
  render(<RailHost />, { onCaughtError });

  expect(await screen.findByText("rail host mounted")).toBeTruthy();
  expect(vi.mocked(loadRailHost).mock.calls).toEqual([[false], [true]]);
  expect(onCaughtError).toHaveBeenCalledTimes(1);
  expect(onCaughtError.mock.calls[0]?.[0]).toBe(chunkError);
});

test("a retry that fails again offers a page reload instead of stranding the sidebar", async () => {
  const chunkError = new Error(CHUNK_ERROR);
  const onCaughtError = captureExpectedError(chunkError);
  vi.mocked(loadRailHost).mockRejectedValue(chunkError);
  const reload = vi.fn();
  vi.stubGlobal("location", { ...window.location, reload });
  const user = userEvent.setup();

  render(<RailHost />, { onCaughtError });
  await screen.findByText("Couldn't load the sidebar");
  // The first failure offers only the cache-busted retry: a deploy that
  // removed the hashed chunk is still only one hypothesis among transient
  // ones, so the reload is the second-strike path, not the first.
  expect(screen.queryByRole("button", { name: "Reload page" })).toBeNull();

  await user.click(screen.getByRole("button", { name: "Retry" }));
  await user.click(await screen.findByRole("button", { name: "Reload page" }));

  expect(reload).toHaveBeenCalledTimes(1);
  expect(vi.mocked(loadRailHost).mock.calls).toEqual([[false], [true]]);
  expect(onCaughtError).toHaveBeenCalledTimes(2);
  expect(onCaughtError.mock.calls[0]?.[0]).toBe(chunkError);
  expect(onCaughtError.mock.calls[1]?.[0]).toBe(chunkError);
});

test("an ordinary retry failure does not prescribe a page reload", async () => {
  // A chunk fetch that fails WITHOUT naming a stale hashed asset (a 500
  // page's HTML where the chunk bytes should be, a proxy error page) is not
  // a chunk-load failure: the rail boundary declines it, so it lands on the
  // next boundary above - never the rail failure state, and so never the
  // Retry/Reload pair that could misreport it as a stale deploy.
  const chunkError = new Error("RailHost chunk request failed with status 500");
  const onCaughtError = captureExpectedError(chunkError);
  vi.mocked(loadRailHost).mockRejectedValue(chunkError);

  render(
    <RailTestOuterBoundary>
      <RailHost />
    </RailTestOuterBoundary>,
    { onCaughtError },
  );

  expect(await screen.findByText("outer boundary caught: RailHost chunk request failed with status 500")).toBeTruthy();
  expect(screen.queryByText("Couldn't load the sidebar")).toBeNull();
  expect(screen.queryByRole("button", { name: "Reload page" })).toBeNull();
  expect(onCaughtError).toHaveBeenCalledTimes(1);
  expect(onCaughtError.mock.calls[0]?.[0]).toBe(chunkError);
  expect(onCaughtError.mock.calls[0]?.[1].errorBoundary).toBeInstanceOf(RailTestOuterBoundary);
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
  const renderError = new Error("RailHost render logic bug");
  const onCaughtError = vi.fn<NonNullable<RenderOptions["onCaughtError"]>>((error, info) => {
    if (error !== renderError || !(info.errorBoundary instanceof RailTestOuterBoundary)) {
      realConsoleError(error, info);
    }
  });
  vi.mocked(loadRailHost).mockResolvedValue({
    RailHost: () => {
      throw renderError;
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
  expect(onCaughtError).toHaveBeenCalledTimes(1);
  expect(onCaughtError.mock.calls[0]?.[0]).toBe(renderError);
  expect(onCaughtError.mock.calls[0]?.[1].errorBoundary).toBeInstanceOf(RailTestOuterBoundary);
});
