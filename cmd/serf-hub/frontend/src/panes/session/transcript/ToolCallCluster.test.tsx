// The tool-call CLUSTER's own collapsed header - the "N steps · <lead call's
// summary>" line a folded run of adjacent calls shows before anyone opens it
// (ToolCallCluster.tsx). It is a THIRD rendering of a lead descriptor's own
// summary text, beside ToolCallItem's per-call row and the descriptor's own
// expanded body, and until kata 79cs it was the one that still showed a
// web_fetch URL as dead text.
//
// The fixtures below use REAL registered tool names, not the `xxx_test_tool`
// shorthand the other transcript test files register inline: the cluster picks
// its lead with consequenceRank, which ranks an unregistered name
// "destructive" on purpose (see consequenceRank.ts), so an invented tool name
// would always win the lead and no fixture could ever put a web_fetch call in
// front.
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { resetDisclosureStoreForTests } from "../../../widgets/disclosure/disclosureStore";
import { ToolCallCluster } from "./ToolCallCluster";
import "./tools/webTools"; // the real web_fetch descriptor (summary + summaryLink)
import "./tools/fsTools"; // read_file, the read-only cluster-mate
import "./tools/editTools"; // write_file, the higher-consequence lead below

// An opened cluster mounts a real ToolCallItem per call, whose disclosure
// state lives in the shared store keyed by item.id - it must not leak from the
// toggle test into another test's row of the same id.
afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

const turn: TurnModel = { id: "turn_1", status: "inProgress", items: [] };

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

function webFetch(url: string, id = "fetch"): ItemModel {
  return item({
    id,
    toolName: "web_fetch",
    argumentsJSON: JSON.stringify({ url, question: "q" }),
    output: JSON.stringify({ answer: "hi", size_bytes: 4096 }),
    status: "completed",
  });
}

function readFile(id: string, path: string): ItemModel {
  return item({ id, toolName: "read_file", argumentsJSON: JSON.stringify({ file_path: path }), status: "completed" });
}

function writeFile(id: string, path: string): ItemModel {
  return item({ id, toolName: "write_file", argumentsJSON: JSON.stringify({ file_path: path }), status: "completed" });
}

// web_fetch, web_search, read_file, grep and friends all rank "read-only"
// (consequenceRank.ts), and leadItem breaks that tie toward the run's FIRST
// item - so a read-only run that happens to start with a web_fetch call
// hoists that call's own "Fetched <url> · N bytes" text into the cluster's
// collapsed header. kata xw3t linkified the same text on the per-call row and
// in the expanded body; this header showed it inert.
const READ_ONLY_RUN_LED_BY_FETCH = (url: string) => [
  webFetch(url),
  readFile("read-a", "src/cache.go"),
  readFile("read-b", "src/store.go"),
];

test("kata 79cs: a cluster led by a web_fetch call links that URL in its own collapsed header", () => {
  render(<ToolCallCluster items={READ_ONLY_RUN_LED_BY_FETCH("https://example.com/page")} turn={turn} />);
  const link = screen.getByRole("link", { name: "https://example.com/page" }) as HTMLAnchorElement;
  expect(link.getAttribute("href")).toBe("https://example.com/page");
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener noreferrer");
  // The header still READS exactly as it did - the "N steps · " count and the
  // lead's own summary, whole, with no text lost or duplicated around the
  // anchor - and the run is still folded: this is the pre-expansion line.
  expect(screen.getByTestId("tool-row-summary").textContent).toBe(
    "3 steps · Fetched https://example.com/page · 4096 bytes",
  );
  expect((screen.getByTestId("tool-call-cluster") as HTMLDetailsElement).open).toBe(false);
});

// The header IS a native <summary> (ToolRow's expandable branch) whose own
// onClick unconditionally preventDefaults and toggles. Without the anchor
// stopping propagation, that same bubbled event would cancel the link's
// navigation as well as unfold the run - a link that looks clickable and does
// neither.
test("kata 79cs: clicking the header's link opens the URL - it must not unfold the cluster", () => {
  render(<ToolCallCluster items={READ_ONLY_RUN_LED_BY_FETCH("https://example.com/page")} turn={turn} />);
  const cluster = screen.getByTestId("tool-call-cluster") as HTMLDetailsElement;
  fireEvent.click(screen.getByRole("link"));
  expect(cluster.open).toBe(false);
  expect(screen.queryByTestId("tool-call-cluster-body")).toBeNull();
  // The rest of the header still unfolds the run, unaffected.
  fireEvent.click(screen.getByTestId("tool-row"));
  expect(cluster.open).toBe(true);
  expect(screen.getAllByTestId("tool-call-item")).toHaveLength(3);
});

// The http(s)-only rule is the descriptor's (webFetchLink, shared with the
// expanded body since tcp9), so this surface inherits it rather than deciding
// again: an anchor built from tool-call text must never be able to carry a
// javascript:/data: href.
test("kata 79cs: a non-http(s) lead URL stays plain text in the header - never a dangerous anchor", () => {
  render(<ToolCallCluster items={READ_ONLY_RUN_LED_BY_FETCH("javascript:alert(1)")} turn={turn} />);
  expect(screen.queryByRole("link")).toBeNull();
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("3 steps · Fetched javascript:alert(1) · 4096 bytes");
});

// The header names ONE call - the highest-consequence one - and the link has
// to point at that same call. A run whose fetch is not the lead shows the
// lead's own text, so linking the fetch's URL here would be a link pointing
// somewhere the visible words don't say.
test("kata 79cs: the header's link follows the LEAD call, not just any web_fetch in the run", () => {
  const items = [
    webFetch("https://example.com/page"),
    writeFile("write", "src/cache.go"),
    readFile("read", "src/x.go"),
  ];
  render(<ToolCallCluster items={items} turn={turn} />);
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("3 steps · Wrote src/cache.go");
  expect(screen.queryByRole("link")).toBeNull();
});
