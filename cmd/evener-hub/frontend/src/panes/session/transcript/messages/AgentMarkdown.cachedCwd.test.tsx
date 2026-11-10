import type { ThreadModel } from "@evener/appwire-client";
import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { TranscriptRenderProvider } from "../../../../transcriptDisplay/renderContext";
import { openDocBeside } from "../../../doc/openDoc";
import { AgentMarkdown } from "./AgentMarkdown";

vi.mock("../../../doc/openDoc", () => ({ openDocBeside: vi.fn() }));

const CONFIG = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
const SESSION_ID = "02wMz5Txv1C3Hut0M8GCeB";
const REF = `local:${SESSION_ID}`;

function thread(cwd: string): ThreadModel {
  return { ref: REF, cwd, artifactsDir: "", scratchDir: "" } as unknown as ThreadModel;
}

function view(cwd: string) {
  return (
    <TranscriptRenderProvider config={CONFIG} thread={thread(cwd)} sessionRef={REF} sourcePaneId="pane-1">
      <AgentMarkdown source="[COMIC_DOCUMENT_CHILD](comic-document-child.md)" />
    </TranscriptRenderProvider>
  );
}

afterEach(() => {
  cleanup();
  vi.mocked(openDocBeside).mockReset();
});

test("a filename click before cwd is held and opens the source-owned file once cwd arrives", () => {
  const { container, rerender } = render(view(""));
  const anchor = container.querySelector("a[href]") as HTMLAnchorElement;
  const click = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 });
  act(() => {
    anchor.dispatchEvent(click);
  });
  expect(click.defaultPrevented).toBe(true);
  expect(openDocBeside).not.toHaveBeenCalled();

  rerender(view("/projects/demo"));
  expect(openDocBeside).toHaveBeenCalledTimes(1);
  const request = vi.mocked(openDocBeside).mock.calls[0]![0] as {
    session: string;
    reference: { path: string };
  };
  expect(request.session).toBe(REF);
  expect(request.reference.path).toBe("comic-document-child.md");
});

test("a cwd-ready link keeps the existing direct activation", () => {
  const { container } = render(view("/projects/demo"));
  const anchor = container.querySelector("a[href]") as HTMLAnchorElement;
  expect(anchor.getAttribute("href")).toContain("/doc/");
  const click = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 });
  act(() => {
    anchor.dispatchEvent(click);
  });
  expect(openDocBeside).toHaveBeenCalledTimes(1);
});
