import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { UseTranscriptResult } from "../panes/session/transcript/useTranscript";
import { ComicTranscript } from "./ComicTranscript";
import type { ComicEntry } from "./projection";
import { readComicPosition, writeComicPosition } from "./readerPosition";
import { useComicReader } from "./useComicReader";

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  // jsdom has no layout. This seam exercises real VirtualList windowing, not geometry acceptance.
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(500);
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(760);
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

const entry: ComicEntry = {
  id: "m",
  sourceItemId: "source-m",
  sourceIndex: 0,
  kind: "dialogue",
  speaker: "human",
  sourceLabel: "FIXTURE_SOURCE",
  text: "literal fixture *text*",
  streaming: false,
};
function history(): UseTranscriptResult {
  return {
    model: undefined,
    hasOlder: false,
    loadOlder: vi.fn(async () => {}),
    loadingOlder: false,
    cancelOlder: vi.fn(),
    loadOlderReportingError: vi.fn(),
    olderError: null,
  };
}
function Reader({
  entries = [entry],
  mode = "comic",
  older,
}: {
  entries?: readonly ComicEntry[];
  mode?: "comic" | "plain";
  older: UseTranscriptResult;
}) {
  const reader = useComicReader("local:test");
  return <ComicTranscript entries={entries} history={older} reader={reader} mode={mode} />;
}
test("uses one scroll port through view changes and cancels restoration at latest", async () => {
  const older = history();
  const mounted = render(<Reader older={older} />);
  const port = mounted.container.querySelector("[data-comic-scroll]");
  expect(port).not.toBeNull();
  mounted.rerender(<Reader older={older} mode="plain" />);
  expect(mounted.container.querySelector("[data-comic-scroll]")).toBe(port);
  fireEvent.click(screen.getByRole("button", { name: /latest/i }));
  expect(older.cancelOlder).toHaveBeenCalled();
  mounted.unmount();
  expect(document.querySelector("[data-comic-probe]")).toBeNull();
});
test("retains an absent saved anchor while shared pages advance, and stops on failure or user movement", async () => {
  writeComicPosition("local:test", {
    mode: "away",
    sessionRef: "local:test",
    path: [],
    entryId: "older",
    offset: 7,
    viewportOffset: 24,
  });
  const older = { ...history(), hasOlder: true };
  const mounted = render(<Reader older={older} />);
  await waitFor(() => expect(older.loadOlderReportingError).toHaveBeenCalledTimes(1));
  mounted.rerender(<Reader older={{ ...older, loadingOlder: true }} />);
  expect(older.loadOlderReportingError).toHaveBeenCalledTimes(1);
  mounted.rerender(
    <Reader older={{ ...older, olderError: "FIXTURE_FAILURE" }} entries={[{ ...entry, id: "page-two" }, entry]} />,
  );
  expect(older.loadOlderReportingError).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: /retry/i }));
  expect(older.loadOlderReportingError).toHaveBeenCalledTimes(2);
  fireEvent.wheel(mounted.container.querySelector("[data-comic-scroll]")!.firstElementChild!);
  expect(older.cancelOlder).toHaveBeenCalled();
  mounted.unmount();
  sessionStorage.clear();
});

test("keeps itemless errors in distinct canonical groups through switching and prepending", async () => {
  const error: ComicEntry = {
    id: "turn-error:t0",
    sourceTurnId: "t0",
    kind: "caption",
    speaker: "source",
    sourceLabel: "FIXTURE_ERROR",
    text: "FIXTURE_FAILED_TURN",
    streaming: false,
  };
  const older = history();
  const mounted = render(<Reader entries={[entry, error]} older={older} />);
  await waitFor(() => expect(mounted.container.querySelector('[data-comic-turn-id="t0"]')).not.toBeNull());
  for (const mode of ["plain", "comic"] as const) {
    mounted.rerender(<Reader entries={[{ ...entry, id: "preceding" }, entry, error]} mode={mode} older={older} />);
    await waitFor(() =>
      expect(mounted.container.querySelector('[data-comic-entry="m"] [data-comic-text]')?.textContent).toBe(entry.text),
    );
    expect(
      mounted.container
        .querySelector('[data-comic-entry="turn-error:t0"]')
        ?.closest('section[aria-label="Turn errors"]'),
    ).not.toBeNull();
    expect(mounted.container.querySelector('[data-comic-entry="turn-error:t0"] [data-comic-text]')?.textContent).toBe(
      error.text,
    );
  }
  mounted.unmount();
});

test("mounts bounded source rows rather than the whole loaded history", async () => {
  const entries = Array.from({ length: 500 }, (_, index) => ({ ...entry, id: `m-${index}`, sourceIndex: index }));
  const mounted = render(<Reader entries={entries} older={history()} />);
  await waitFor(() => expect(mounted.container.querySelectorAll("[data-comic-entry]").length).toBeGreaterThan(0));
  expect(mounted.container.querySelectorAll("[data-comic-entry]").length).toBeLessThan(30);
  mounted.unmount();
});

test("settles an unavailable saved source when history is exhausted", async () => {
  writeComicPosition("local:test", {
    mode: "away",
    sessionRef: "local:test",
    path: [],
    entryId: "deleted",
    offset: 12,
    viewportOffset: 24,
  });
  const older = history();
  const mounted = render(<Reader older={older} />);
  await waitFor(() =>
    expect(readComicPosition("local:test")).toEqual({
      mode: "away",
      sessionRef: "local:test",
      path: [],
      entryId: "m",
      offset: 0,
      viewportOffset: 24,
    }),
  );
  expect(older.loadOlderReportingError).not.toHaveBeenCalled();
  mounted.unmount();
});

test("selection cancels missing-source demand before another page can be requested", async () => {
  writeComicPosition("local:test", {
    mode: "away",
    sessionRef: "local:test",
    path: [],
    entryId: "missing",
    offset: 12,
    viewportOffset: 24,
  });
  const older = { ...history(), hasOlder: true };
  const mounted = render(<Reader older={older} />);
  await waitFor(() => expect(older.loadOlderReportingError).toHaveBeenCalledTimes(1));
  const text = mounted.container.querySelector("[data-comic-text]")!;
  const range = document.createRange();
  range.selectNodeContents(text);
  document.getSelection()?.addRange(range);
  fireEvent(document, new Event("selectionchange"));
  expect(older.cancelOlder).toHaveBeenCalledTimes(1);
  mounted.rerender(<Reader older={older} entries={[{ ...entry, id: "page-two" }, entry]} />);
  expect(older.loadOlderReportingError).toHaveBeenCalledTimes(1);
  document.getSelection()?.removeAllRanges();
  mounted.unmount();
});
