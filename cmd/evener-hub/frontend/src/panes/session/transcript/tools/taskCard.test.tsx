import type { ItemModel, TurnModel } from "@evener/appwire-client";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { lazy } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { registerPaneForTests } from "../../../../shell/paneRegistry";
import { isPaneOpen, workspaceStore } from "../../../../shell/workspace";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { ToolCallItem } from "../ToolCallItem";
import "./taskCard"; // registers the real "task_list" descriptor

afterEach(() => {
  cleanup();
  // The card settles folded and every test opens it by writing a disclosure
  // store entry keyed by this file's constant item id, so the store must be
  // reset between tests or a prior test's open state leaks into the next.
  resetDisclosureStoreForTests();
});

const turn: TurnModel = { id: "turn_1", status: "completed", items: [] };

// A task_list commandExecution item, matching the wire the reducer preserves:
// argumentsJSON (kept through settle by mergeArguments / on reload by
// apptranscript.go) + the tool's own output text (which carries the
// "Progress: …" footer for append/update) + the authoritative Task[] snapshot
// riding raw (agent/task/task_store.go, snake_case json tags).
function taskItem(args: unknown, output = "", overrides: Partial<ItemModel> = {}): ItemModel {
  return {
    id: "item_1",
    turnId: "turn_1",
    type: "commandExecution",
    toolName: "task_list",
    text: "",
    argumentsJSON: JSON.stringify(args),
    output,
    ...overrides,
  };
}

function renderItem(item: ItemModel, sessionRef?: string) {
  return render(<ToolCallItem item={item} turn={turn} live={false} sessionRef={sessionRef} />);
}

// The reworked card settles folded at every verbosity level (the descriptor's
// foldByDefault), so body assertions open the row first - the reader's own
// click, through the same disclosure store the production row uses.
function openRow(): void {
  fireEvent.click(screen.getByTestId("tool-row-trigger"));
}

function rowIsOpen(): boolean {
  return screen.getByTestId("tool-row-trigger").getAttribute("aria-expanded") === "true";
}

// A realistic StateResult.State snapshot (agent/task/task_store.go's Task[]
// shape): 7 tasks, the first four done, #5 in_progress, #6/#7 open. No
// timestamps, so the window's settled slot falls back to list order (the last
// done row) - the same degradation a raw-less replay gets.
function sevenTaskState(overrides: Partial<Record<number, Record<string, unknown>>> = {}) {
  const base = [
    { id: 1, type: "implement", description: "first", prompt: "", status: "done" },
    { id: 2, type: "implement", description: "second", prompt: "", status: "done" },
    { id: 3, type: "implement", description: "third", prompt: "", status: "done" },
    { id: 4, type: "implement", description: "fourth", prompt: "", status: "done" },
    { id: 5, type: "implement", description: "fifth", prompt: "", status: "in_progress" },
    { id: 6, type: "implement", description: "sixth", prompt: "", status: "open" },
    { id: 7, type: "implement", description: "seventh", prompt: "", status: "open" },
  ];
  return base.map((t) => (overrides[t.id] ? { ...t, ...overrides[t.id] } : t));
}

// The canonical completing-a-task call: task #4 completes, the daemon
// auto-advances #5, so the folded line names the start ("→ fifth") and the
// open window holds fourth/fifth/sixth. Most tests start from this shape.
function mainUpdate(): ItemModel {
  return taskItem(
    { action: "update", updates: [{ id: 4, status: "done" }] },
    "Updated 4→done. Progress: 4/7 tasks complete.",
    { raw: sevenTaskState({ 5: { started: true } }) },
  );
}

// ---- suppression: what renders at all --------------------------------

test('action:"view" renders nothing at all (no card, no divider, no tool-call row)', () => {
  renderItem(taskItem({ action: "view" }, "1. [open] implement — a\n\nProgress: 0/1 tasks complete."));
  expect(screen.queryByTestId("tool-call-item")).toBe(null);
  expect(screen.queryByTestId("task-card")).toBe(null);
});

test("a failed task_list mutation renders NO card (its error is surfaced by the generic tool-error path instead)", () => {
  renderItem(taskItem({ action: "update", updates: [{ id: 9, status: "done" }] }, "", { error: "task 9 not found" }));
  // The row still exists (the generic error path owns it), but no task card.
  expect(screen.getByTestId("tool-call-item")).toBeTruthy();
  expect(screen.queryByTestId("task-card")).toBe(null);
  expect(screen.getByText("task 9 not found")).toBeTruthy();
});

test("a malformed / non-mutation task_list with no error renders nothing", () => {
  renderItem(taskItem({ action: "append" }, "")); // append with no tasks array = invalid
  expect(screen.queryByTestId("tool-call-item")).toBe(null);
});

test("an update that changes no task status renders nothing (a reopen, a notes-only touch)", () => {
  // Wire-true fixture: the Go tool rejects a status-less update, so the real
  // status-less mutations are a reopen to "open" and a note-carrying
  // reassertion. Under the rework the card exists to carry UPDATES; with no
  // touch to name, neither the folded line nor the recap has anything true
  // to say, so the whole row stays suppressed.
  const { unmount } = renderItem(
    taskItem({ action: "update", updates: [{ id: 1, status: "open", notes: "added a caveat" }] }, "Updated 1→open."),
  );
  expect(screen.queryByTestId("tool-call-item")).toBe(null);
  unmount();
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 1, status: "in_progress", notes: "found the root cause" }] },
      "Updated 1→in_progress.",
      {
        raw: [
          {
            id: 1,
            type: "implement",
            description: "investigate",
            prompt: "inspect",
            status: "in_progress",
            started: false,
          },
        ],
      },
    ),
  );
  expect(screen.queryByTestId("tool-call-item")).toBe(null);
});

test("a reasserted terminal settle renders nothing (the store kept the original stamp)", () => {
  // The mutation snapshot's settled:false marker says this call did not
  // transition the task - the daemon preserves its original CompletedAt on
  // reassertions - so a re-sent done with a note is an annotation, not
  // news: no row to name, the whole card stays suppressed. The tasks pane
  // carries the note.
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 1, status: "done", notes: "still done" }] },
      "Updated 1→done. Progress: 1/1 tasks complete.",
      {
        raw: [
          {
            id: 1,
            type: "implement",
            description: "already finished",
            prompt: "finish",
            status: "done",
            settled: false,
          },
        ],
      },
    ),
  );
  expect(screen.queryByTestId("tool-call-item")).toBe(null);
});

test("a reasserted settle in a mixed batch yields only the real completion", () => {
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 2, status: "done" },
          { id: 1, status: "done", notes: "still done" },
        ],
      },
      "Updated 2→done, 1→done. Progress: 2/2 tasks complete.",
      {
        raw: [
          { id: 1, type: "implement", description: "already finished", prompt: "", status: "done", settled: false },
          { id: 2, type: "implement", description: "real completion", prompt: "", status: "done", settled: true },
        ],
      },
    ),
  );
  // #1's settled:false marker suppresses its reassertion; #2's real
  // completion is the batch's final word, alone.
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("☑ real completion");
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows).toHaveLength(1);
  expect(rows[0]!.textContent).toContain("real completion");
});

test("a batch that touches a task away from terminal and back renders the fresh settle", () => {
  // done -> open -> done in one batch: the store stamps a fresh CompletedAt
  // (the open touch cleared it, the done touch minted a new one) and the
  // snapshot marks the task settled, so the card renders the settle as
  // news. The marker and the stamp must agree or this batch would suppress
  // genuine work.
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 1, status: "open" },
          { id: 1, status: "done" },
        ],
      },
      "Updated 1→open, 1→done. Progress: 1/1 tasks complete.",
      {
        raw: [
          {
            id: 1,
            type: "implement",
            description: "bounced settle",
            prompt: "",
            status: "done",
            settled: true,
            completed_at: "2026-09-23T15:00:00Z",
            updated_at: "2026-09-23T15:00:01Z",
          },
        ],
      },
    ),
  );
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("☑ bounced settle");
});

test("a pure notes update on an open task renders nothing (no status changed)", () => {
  renderItem(
    taskItem({ action: "update", updates: [{ id: 6, status: "open", notes: "still blocked" }] }, "Updated 6→open.", {
      raw: sevenTaskState(),
    }),
  );
  expect(screen.queryByTestId("tool-call-item")).toBe(null);
});

// ---- the folded line: only the most recent update ---------------------

test("a settled mutation lands folded: no body, and the summary line names only the latest update", () => {
  renderItem(mainUpdate());
  expect(rowIsOpen()).toBe(false);
  expect(screen.queryByTestId("tool-call-body")).toBeNull();
  // The completion caused the daemon to auto-advance #5, so the most recent
  // update is the start - the folded line names it with its mark.
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("→ fifth");
});

test("opening the row swaps the summary line for a recap of this call's whole change", () => {
  renderItem(mainUpdate());
  openRow();
  expect(screen.getByTestId("tool-row-summary").textContent).toBe('Completed "fourth"; started "fifth"');
  // Folding again restores the latest-update line - the swap is a display
  // state, not a one-way replacement.
  openRow();
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("→ fifth");
});

test("a completion with no auto-advance folds to the completed task itself", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 7, status: "done" }] },
      "Updated 7→done. All tasks complete. Progress: 7/7 tasks complete.",
      { raw: sevenTaskState({ 5: { status: "done" }, 6: { status: "done" }, 7: { status: "done" } }) },
    ),
  );
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("☑ seventh");
});

test("a cancellation folds to the dropped task with its mark", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 4, status: "cancelled" }] },
      "Updated 4→cancelled. Progress: 4/7 tasks complete.",
      {
        // #5 was already in progress before this call, so its started:false
        // marker is the wire-true shape - the daemon auto-advances only when
        // something eligible is left, and a pre-existing current task is not
        // something THIS call started.
        raw: sevenTaskState({ 4: { status: "cancelled" }, 5: { started: false } }),
      },
    ),
  );
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("☒ fourth");
});

test("an append folds to the last added task with the added mark", () => {
  renderItem(
    taskItem(
      { action: "append", tasks: [{ type: "implement", description: "build the thing" }] },
      "Added 1 task(s). Progress: 0/1 tasks complete.",
    ),
  );
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("☐ build the thing");
});

test("a batch that explicitly completes one task and starts another folds to the start", () => {
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 4, status: "done" },
          { id: 5, status: "in_progress" },
        ],
      },
      "Updated 4→done, 5→in_progress. Progress: 4/7 tasks complete.",
      { raw: sevenTaskState() },
    ),
  );
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("→ fifth");
  openRow();
  expect(screen.getByTestId("tool-row-summary").textContent).toBe('Completed "fourth"; started "fifth"');
});

test("a mixed add and update batch recaps every touch, same-verb labels comma-joined", () => {
  renderItem(
    taskItem(
      { add: [{ type: "implement", description: "newly planned work" }], update: [{ id: 4, status: "done" }] },
      "Added 1 task(s). Updated 4→done. Progress: 4/7 tasks complete.",
      { raw: sevenTaskState({ 5: { started: true } }) },
    ),
  );
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("→ fifth");
  openRow();
  expect(screen.getByTestId("tool-row-summary").textContent).toBe(
    'Added "newly planned work"; completed "fourth"; started "fifth"',
  );
});

test("a historical markerless snapshot retains auto-start inference in the folded line", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 4, status: "done" }] },
      "Updated 4→done. Progress: 4/7 tasks complete.",
      {
        raw: sevenTaskState(),
      },
    ),
  );
  // No `started` marker anywhere: the legacy inference must still find #5 as
  // the task this call advanced to, so the folded line names the start.
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("→ fifth");
});

test("completing a non-current task does not mistake the existing current task for an auto-start", () => {
  renderItem(
    taskItem({ update: [{ id: 3, status: "done" }] }, "Updated 3→done. Progress: 2/3 tasks complete.", {
      raw: [
        { id: 1, type: "implement", description: "first", prompt: "", status: "done" },
        { id: 2, type: "implement", description: "current", prompt: "", status: "in_progress", started: false },
        { id: 3, type: "implement", description: "non-current", prompt: "", status: "done" },
      ],
    }),
  );
  // #2's started:false marker says this call did not start it, so the most
  // recent update is the completion itself.
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("☑ non-current");
});

test("a reasserted current task is touched before its started row is suppressed", () => {
  renderItem(
    taskItem(
      {
        update: [
          { id: 3, status: "done" },
          { id: 2, status: "in_progress", notes: "still working" },
        ],
      },
      "Updated 3→done, 2→in_progress. Progress: 2/3 tasks complete.",
      {
        raw: [
          { id: 1, type: "implement", description: "first", prompt: "", status: "done" },
          { id: 2, type: "implement", description: "current", prompt: "", status: "in_progress", started: false },
          { id: 3, type: "implement", description: "non-current", prompt: "", status: "done" },
        ],
      },
    ),
  );
  // The explicit in_progress reassertion on #2 is suppressed as a row (the
  // false marker), but it is still TOUCHED, so #2 cannot be rediscovered as
  // an auto-start: the folded line names the completion.
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("☑ non-current");
});

test("a real in_progress transition folds to the started task from its authoritative marker", () => {
  renderItem(
    taskItem({ action: "update", updates: [{ id: 1, status: "in_progress" }] }, "Updated 1→in_progress.", {
      raw: [
        { id: 1, type: "implement", description: "implement", prompt: "build", status: "in_progress", started: true },
      ],
    }),
  );
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("→ implement");
});

test("a duplicate in_progress then done update folds to the final done touch", () => {
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 1, status: "in_progress" },
          { id: 1, status: "done" },
        ],
      },
      "Updated 1→in_progress, 1→done. Progress: 1/1 tasks complete.",
      { raw: [{ id: 1, type: "implement", description: "finish", prompt: "finish", status: "done" }] },
    ),
  );
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("☑ finish");
});

test("a batch that completes and reopens the same task renders nothing (net no status change)", () => {
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 1, status: "done" },
          { id: 1, status: "open" },
        ],
      },
      "Updated 1→done, 1→open. Progress: 0/1 tasks complete.",
      { raw: [{ id: 1, type: "implement", description: "reopened", prompt: "", status: "open" }] },
    ),
  );
  // done→open inside one call is a round trip: the task ends where it
  // started, so the card has no update to name. The per-id dedup must treat
  // the reopen as the latest update, not keep the touched-away completion.
  expect(screen.queryByTestId("tool-call-item")).toBe(null);
});

test("distinct final occurrences retain their own order when an earlier duplicate moves later", () => {
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 1, status: "in_progress" },
          { id: 2, status: "done" },
          { id: 1, status: "done" },
        ],
      },
      "Updated 1→in_progress, 2→done, 1→done. Progress: 2/2 tasks complete.",
      {
        raw: [
          { id: 1, type: "implement", description: "first", prompt: "first", status: "done" },
          { id: 2, type: "implement", description: "second", prompt: "second", status: "done" },
        ],
      },
    ),
  );
  // #1's final occurrence lands after #2's, so #1's done is the most recent
  // update; the recap keeps final-occurrence order within its verb group.
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("☑ first");
  openRow();
  expect(screen.getByTestId("tool-row-summary").textContent).toBe('Completed "second", "first"');
});

// ---- the open body: the three-slot window -----------------------------

test("the window shows the most recently completed task, the in-progress one, and the next one, in that order", () => {
  renderItem(mainUpdate());
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows.map((row) => row.getAttribute("data-kind"))).toEqual(["settled", "current", "next"]);
  expect(rows.map((row) => row.textContent)).toEqual([
    expect.stringContaining("fourth"),
    expect.stringContaining("fifth"),
    expect.stringContaining("sixth"),
  ]);
});

test("every window row leads with a TaskCheck glyph matching its slot's state", () => {
  renderItem(mainUpdate());
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(within(rows[0]!).getByTestId("task-check").getAttribute("data-touch")).toBe("done");
  expect(within(rows[1]!).getByTestId("task-check").getAttribute("data-touch")).toBe("started");
  expect(within(rows[2]!).getByTestId("task-check").getAttribute("data-touch")).toBe("pending");
});

test("a cancellation holds the settled slot, struck, with its fresh note", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 2, status: "cancelled", notes: "superseded by #5" }] },
      "Updated 2→cancelled. Progress: 0/1 tasks complete.",
      { raw: [{ id: 2, type: "implement", description: "old approach", prompt: "", status: "cancelled" }] },
    ),
  );
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows).toHaveLength(1);
  const row = rows[0]!;
  expect(row.getAttribute("data-kind")).toBe("settled");
  expect(within(row).getByTestId("task-check").getAttribute("data-touch")).toBe("cancelled");
  expect(within(row).getByText("old approach")).toBeTruthy();
  expect(within(row).getByText("old approach").className).toContain("descStruck");
  expect(within(row).getByText("superseded by #5").className).toContain("note");
});

test("a stampless snapshot's settled slot names the task this call completed, not list order", () => {
  // Legacy rows carry no completed_at, so every settle key ties and the
  // window used to fall back to the LAST settled row in list order - which
  // can be a different, older task than the one this call just completed,
  // while the folded summary names the right one. The touched terminal task
  // wins the tie so both views agree, and the call's fresh note rides the
  // row it belongs to.
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 1, status: "done", notes: "just finished" }] },
      "Updated 1→done. Progress: 1/1 tasks complete.",
      {
        raw: [
          {
            id: 1,
            type: "implement",
            description: "the one just completed",
            prompt: "",
            status: "done",
            settled: true,
          },
          { id: 2, type: "implement", description: "still open", prompt: "", status: "open" },
          { id: 3, type: "implement", description: "older done", prompt: "", status: "done" },
        ],
      },
    ),
  );
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows[0]!.textContent).toContain("the one just completed");
  expect(rows[0]!.textContent).toContain("just finished");
  expect(rows[0]!.textContent).not.toContain("older done");
});

test("the window shrinks when slots are empty: no settled task, no next task", () => {
  const { unmount } = renderItem(
    taskItem({ action: "update", updates: [{ id: 1, status: "in_progress" }] }, "Updated 1→in_progress.", {
      raw: [
        { id: 1, type: "implement", description: "implement", prompt: "build", status: "in_progress", started: true },
      ],
    }),
  );
  openRow();
  let rows = screen.getAllByTestId("task-card-row");
  expect(rows.map((row) => row.getAttribute("data-kind"))).toEqual(["current"]);
  unmount();

  // The reader's toggle survives a remount (the shared disclosure store), so
  // the second same-id item would inherit the open state and the click below
  // would fold it back - reset the store to render the second item folded.
  resetDisclosureStoreForTests();
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 7, status: "done" }] },
      "Updated 7→done. All tasks complete. Progress: 7/7 tasks complete.",
      { raw: sevenTaskState({ 5: { status: "done" }, 6: { status: "done" }, 7: { status: "done" } }) },
    ),
  );
  openRow();
  rows = screen.getAllByTestId("task-card-row");
  expect(rows.map((row) => row.getAttribute("data-kind"))).toEqual(["settled"]);
});

test("window ink: settled is struck, the working task is emphasized, the next one is quiet", () => {
  renderItem(mainUpdate());
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(within(rows[0]!).getByText("fourth").className).toContain("descStruck");
  expect(within(rows[1]!).getByText("fifth").className).toContain("descNow");
  expect(within(rows[2]!).getByText("sixth").className).toContain("descNext");
});

test("the settled slot orders by parsed time: same-second fractional stamps compare chronologically, not as strings", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 2, status: "done", notes: "the fresh note" }] },
      "Updated 2→done. Progress: 2/2 tasks complete.",
      {
        // RFC3339Nano trims trailing zeros, so same-second stamps come in
        // different LENGTHS: ".5Z" is EARLIER than ".55Z", but byte-order
        // comparison says "5Z" > "55Z" and pins the older task in the
        // settled slot - dropping the fresh note riding the newer settle.
        raw: [
          {
            id: 1,
            type: "implement",
            description: "earlier fractional",
            prompt: "",
            status: "done",
            completed_at: "2026-09-23T14:58:00.5Z",
          },
          {
            id: 2,
            type: "implement",
            description: "later fractional",
            prompt: "",
            status: "done",
            completed_at: "2026-09-23T14:58:00.55Z",
          },
          { id: 3, type: "implement", description: "current", prompt: "", status: "in_progress", started: true },
        ],
      },
    ),
  );
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows.map((row) => row.getAttribute("data-kind"))).toEqual(["settled", "current"]);
  expect(within(rows[0]!).getByText("later fractional")).toBeTruthy();
  expect(within(rows[0]!).getByText("the fresh note")).toBeTruthy();
});

test("the settled slot orders by parsed time across differing UTC offsets", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 1, status: "done" }] },
      "Updated 1→done. Progress: 2/2 tasks complete.",
      {
        // 14:58+02:00 is 12:58Z - EARLIER than 13:00Z - but byte-order
        // comparison reads the wall-clock digits and picks the earlier one.
        raw: [
          {
            id: 1,
            type: "implement",
            description: "later instant",
            prompt: "",
            status: "done",
            completed_at: "2026-09-23T13:00:00Z",
          },
          {
            id: 2,
            type: "implement",
            description: "earlier instant",
            prompt: "",
            status: "done",
            completed_at: "2026-09-23T14:58:00+02:00",
          },
        ],
      },
    ),
  );
  openRow();
  const row = screen.getAllByTestId("task-card-row")[0]!;
  expect(row.getAttribute("data-kind")).toBe("settled");
  expect(row.textContent).toContain("later instant");
});

test("the settled slot orders by parsed time within the same millisecond", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 1, status: "done" }] },
      "Updated 1→done. Progress: 2/2 tasks complete.",
      {
        // One call can settle two tasks in a single batch, microseconds
        // apart: 900µs and 100µs both parse to the same whole millisecond,
        // so the sub-millisecond digits must still order the pair
        // chronologically rather than falling back to list position.
        raw: [
          {
            id: 1,
            type: "implement",
            description: "later sub-millisecond",
            prompt: "",
            status: "done",
            completed_at: "2026-09-23T14:58:00.0009Z",
          },
          {
            id: 2,
            type: "implement",
            description: "earlier sub-millisecond",
            prompt: "",
            status: "done",
            completed_at: "2026-09-23T14:58:00.0001Z",
          },
        ],
      },
    ),
  );
  openRow();
  const row = screen.getAllByTestId("task-card-row")[0]!;
  expect(row.getAttribute("data-kind")).toBe("settled");
  expect(row.textContent).toContain("later sub-millisecond");
});

test("a trailing notes-only touch on the same id keeps the card's completion", () => {
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 1, status: "done" },
          { id: 1, notes: "with a caveat" },
        ],
      },
      "Updated 1→done, 1. Progress: 1/1 tasks complete.",
      { raw: [{ id: 1, type: "implement", description: "finish the thing", prompt: "", status: "done" }] },
    ),
  );
  // The batch completed #1 and then annotated it; the per-id dedup must
  // keep the completion, not let the statusless touch erase it into
  // suppression.
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("☑ finish the thing");
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows.map((row) => row.getAttribute("data-kind"))).toEqual(["settled"]);
  // The trailing note this call added rides the row it annotated.
  expect(within(rows[0]!).getByText("with a caveat")).toBeTruthy();
});

test("a notes-only touch arriving before the status word still carries to the raw path's row", () => {
  // The raw path derives fresh notes from the call's own state, not the
  // update order, so both orders must render the note on the row.
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 1, notes: "with a caveat" },
          { id: 1, status: "done" },
        ],
      },
      "Updated 1→done, 1. Progress: 1/1 tasks complete.",
      { raw: [{ id: 1, type: "implement", description: "finish the thing", prompt: "", status: "done" }] },
    ),
  );
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows.map((row) => row.getAttribute("data-kind"))).toEqual(["settled"]);
  expect(within(rows[0]!).getByText("with a caveat")).toBeTruthy();
});

test("when both touches carry notes, the later note wins on the raw path", () => {
  // freshNotes keeps last-wins by batch position: the status word's own
  // note is the later touch here, so it is the one that renders.
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 1, notes: "early note" },
          { id: 1, status: "done", notes: "with a caveat" },
        ],
      },
      "Updated 1→done, 1. Progress: 1/1 tasks complete.",
      { raw: [{ id: 1, type: "implement", description: "finish the thing", prompt: "", status: "done" }] },
    ),
  );
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(within(rows[0]!).getByText("with a caveat")).toBeTruthy();
  expect(within(rows[0]!).queryByText("early note")).toBeNull();
});

test("when both touches carry notes, the later note wins on the fallback row too", () => {
  // Same batch without raw: the fallback must agree with freshNotes'
  // last-wins, not let the earlier notes-only touch clobber the status
  // update's own note.
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 1, notes: "early note" },
          { id: 1, status: "done", notes: "with a caveat" },
        ],
      },
      "Updated 1→done, 1. Progress: 1/1 tasks complete.",
    ),
  );
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows).toHaveLength(1);
  expect(rows[0]!.textContent).toContain("with a caveat");
  expect(rows[0]!.textContent).not.toContain("early note");
});

test("a note on an earlier STATUS touch still rides the batch's later final word on the fallback row", () => {
  // The note came attached to a status update, not a notes-only touch -
  // the raw path's freshNotes carries it (it collects any update bearing
  // notes), so the fallback must too, or the two derivations disagree
  // about which notes belong to the call.
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 1, status: "done", notes: "wrapped up with a caveat" },
          { id: 1, status: "in_progress" },
        ],
      },
      "Updated 1→done, 1→in_progress. Progress: 0/1 tasks complete.",
    ),
  );
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows).toHaveLength(1);
  expect(rows[0]!.getAttribute("data-touch")).toBe("started");
  expect(rows[0]!.textContent).toContain("wrapped up with a caveat");
});

test("a note on an earlier status touch still rides the raw path's row", () => {
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 1, status: "done", notes: "wrapped up with a caveat" },
          { id: 1, status: "in_progress" },
        ],
      },
      "Updated 1→done, 1→in_progress. Progress: 0/1 tasks complete.",
      {
        raw: [
          {
            id: 1,
            type: "implement",
            description: "bouncing task",
            prompt: "",
            status: "in_progress",
            started: true,
          },
        ],
      },
    ),
  );
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(within(rows[0]!).getByText("wrapped up with a caveat")).toBeTruthy();
});

test("a trailing notes-only touch keeps the fallback row's completion and carries the note", () => {
  // Same batch shape with no raw (old daemon / replayed transcript): the
  // fallback row keeps the "#id" label, the done touch, and the note.
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 1, status: "done" },
          { id: 1, notes: "with a caveat" },
        ],
      },
      "Updated 1→done, 1. Progress: 1/1 tasks complete.",
    ),
  );
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("☑ #1");
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows).toHaveLength(1);
  expect(rows[0]!.getAttribute("data-touch")).toBe("done");
  expect(rows[0]!.textContent).toContain("with a caveat");
});

test("a notes-only touch arriving BEFORE the status word carries through to the fallback row too", () => {
  // Same batch with the touches reversed: the note reaches the daemon's
  // task either way (Update appends notes in order), so the no-raw fallback
  // must agree with freshNotes on the raw path and keep the note - the
  // final word stays the status, but the annotation is not discarded.
  renderItem(
    taskItem(
      {
        action: "update",
        updates: [
          { id: 1, notes: "with a caveat" },
          { id: 1, status: "done" },
        ],
      },
      "Updated 1→done, 1. Progress: 1/1 tasks complete.",
    ),
  );
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("☑ #1");
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows).toHaveLength(1);
  expect(rows[0]!.getAttribute("data-touch")).toBe("done");
  expect(rows[0]!.textContent).toContain("with a caveat");
});

test("a cancelled task settles at its terminal stamp, not a later annotation's updated_at", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 2, status: "done" }] },
      "Updated 2→done. Progress: 2/2 tasks complete.",
      {
        // #1 was cancelled at 14:00 and merely annotated at 15:30; #2
        // genuinely settled at 14:59. The window must read the
        // cancellation's terminal stamp, not its inflated updated_at, so
        // the newer completion holds the settled slot.
        raw: [
          {
            id: 1,
            type: "implement",
            description: "stale cancellation",
            prompt: "",
            status: "cancelled",
            completed_at: "2026-09-23T14:00:00Z",
            updated_at: "2026-09-23T15:30:00Z",
          },
          {
            id: 2,
            type: "implement",
            description: "newer completion",
            prompt: "",
            status: "done",
            completed_at: "2026-09-23T14:59:00Z",
            updated_at: "2026-09-23T14:59:00Z",
          },
        ],
      },
    ),
  );
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows.map((row) => row.getAttribute("data-kind"))).toEqual(["settled"]);
  expect(rows[0]!.textContent).toContain("newer completion");
});

test("a legacy cancelled row without a settle stamp never outranks a stamped settle", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 2, status: "done" }] },
      "Updated 2→done. Progress: 1/1 tasks complete.",
      {
        // Task 1 was cancelled by a daemon that stamped no settle time and
        // merely annotated later: its updated_at is not a settle moment,
        // so the newer completion holds the settled slot.
        raw: [
          {
            id: 1,
            type: "implement",
            description: "old cancellation",
            prompt: "",
            status: "cancelled",
            updated_at: "2026-09-23T15:30:00Z",
          },
          {
            id: 2,
            type: "implement",
            description: "newer completion",
            prompt: "",
            status: "done",
            completed_at: "2026-09-23T14:59:00Z",
          },
        ],
      },
    ),
  );
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows.map((row) => row.getAttribute("data-kind"))).toEqual(["settled"]);
  expect(rows[0]!.textContent).toContain("newer completion");
});

test("the status rides along visually-hidden on every window row", () => {
  renderItem(mainUpdate());
  openRow();
  const rows = screen.getAllByTestId("task-card-row");
  expect(within(rows[0]!).getByText("done").className).toContain("srOnly");
  expect(within(rows[1]!).getByText("started").className).toContain("srOnly");
  expect(within(rows[2]!).getByText("pending").className).toContain("srOnly");
});

// ---- notes: only the ones this call added ------------------------------

test("a note this call added renders under its row's label, inside the row's text column", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 4, status: "cancelled", notes: "superseded by #5" }] },
      "Updated 4→cancelled. Progress: 3/7 tasks complete.",
      { raw: sevenTaskState({ 4: { status: "cancelled" } }) },
    ),
  );
  openRow();
  const note = screen.getByText("superseded by #5");
  expect(note.className).toContain("note");
  // The note sits in the same column wrapper as the label, not on the
  // glyph's baseline row.
  expect(note.parentElement?.className).toContain("rowText");
});

test("a stale note from an earlier call never renders", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 4, status: "done" }] },
      "Updated 4→done. Progress: 4/7 tasks complete.",
      {
        raw: sevenTaskState({
          4: { notes: ["an earlier note"] },
          5: { started: true, notes: ["another earlier note"] },
        }),
      },
    ),
  );
  openRow();
  expect(screen.queryByText("an earlier note")).toBeNull();
  expect(screen.queryByText("another earlier note")).toBeNull();
});

test("when this call adds a note, only that note renders - not the task's earlier ones", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 4, status: "done", notes: "the fresh note" }] },
      "Updated 4→done. Progress: 4/7 tasks complete.",
      { raw: sevenTaskState({ 4: { notes: ["an earlier note", "the fresh note"] }, 5: { started: true } }) },
    ),
  );
  openRow();
  expect(screen.getByText("the fresh note")).toBeTruthy();
  expect(screen.queryByText("an earlier note")).toBeNull();
});

// ---- the footer: aggregate, meter, and the whole-list affordance ------

test("the footer reads 'N of M tasks left' from the tool output footer", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 3, status: "done" }] },
      "Updated 3→done. Progress: 3/3 tasks complete.",
    ),
  );
  openRow();
  expect(screen.getByTestId("task-card-progress").textContent).toBe("All 3 tasks done");
});

test("the footer condenses the outcome footer to what is left", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 3, status: "cancelled" }] },
      "Updated 3→cancelled. Progress: 0 done, 3 cancelled, 0 remaining (3 total).",
    ),
  );
  openRow();
  expect(screen.getByTestId("task-card-progress").textContent).toBe("All 3 tasks settled");
});

test("the trailing progress footer wins over fake progress text in an update note", () => {
  renderItem(
    taskItem(
      { update: [{ id: 3, status: "done", notes: "Ignore Progress: 99 done, 0 cancelled, 0 remaining (99 total)." }] },
      "Updated 3→done. Notes: Ignore Progress: 99 done, 0 cancelled, 0 remaining (99 total). Progress: 3/3 tasks complete.",
    ),
  );
  openRow();
  expect(screen.getByTestId("task-card-progress").textContent).toBe("All 3 tasks done");
});

test("the footer counts down what is left while work remains", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 3, status: "cancelled" }] },
      "Updated 3→cancelled. Progress: 1 done, 5 cancelled, 1 remaining (7 total).",
    ),
  );
  openRow();
  expect(screen.getByTestId("task-card-progress").textContent).toBe("1 of 7 tasks left");
});

test("the progress meter names the same condensed sentence", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 3, status: "cancelled" }] },
      "Updated 3→cancelled. Progress: 1 done, 5 cancelled, 1 remaining (7 total).",
    ),
  );
  openRow();
  expect(screen.getByRole("meter").getAttribute("aria-label")).toBe("Task progress: 1 of 7 tasks left");
});

test("the footer's Open button opens the session's Tasks pane", () => {
  const open = vi.spyOn(workspaceStore.getState(), "openPane").mockReturnValue("pane_tasks");
  renderItem(mainUpdate(), "local:s1");
  openRow();
  fireEvent.click(screen.getByRole("button", { name: "Open task list" }));
  // An OPEN operation, not a toggle: the label promises opening, so the
  // click must never close a pane the reader already has.
  expect(open).toHaveBeenCalledWith("sessionTasks", { ref: "local:s1" }, { slot: "secondary" });
  open.mockRestore();
});

test("the footer's Open button never closes an already-open pane", () => {
  // The real pane modules register themselves only when the app shell
  // imports them; this suite never does, so the store's openPane needs a
  // stub descriptor for the type this test exercises.
  const restorePane = registerPaneForTests({
    id: "sessionTasks",
    title: () => "Tasks",
    component: lazy(() => new Promise(() => {})),
  });
  const before = workspaceStore.getState();
  try {
    workspaceStore.getState().openPane("sessionTasks", { ref: "local:s1" }, { slot: "secondary" });
    renderItem(mainUpdate(), "local:s1");
    openRow();
    fireEvent.click(screen.getByRole("button", { name: "Open task list" }));
    expect(isPaneOpen(workspaceStore.getState(), "sessionTasks", { ref: "local:s1" })).toBe(true);
  } finally {
    workspaceStore.setState(before, true);
    restorePane();
  }
});

test("no Open button renders without a session ref (read-only transcript surfaces)", () => {
  renderItem(mainUpdate());
  openRow();
  expect(screen.queryByRole("button", { name: "Open task list" })).toBeNull();
});

// ---- the no-raw fallback: argument-only, never fabricated --------------

test("absent raw (old daemon / replayed transcript) keeps the argument-only rows and the #id labels", () => {
  renderItem(
    taskItem(
      { action: "update", updates: [{ id: 4, status: "done" }] },
      "Updated 4→done. Progress: 4/7 tasks complete.",
    ),
  );
  expect(screen.getByTestId("tool-row-summary").textContent).toBe("☑ #4");
  openRow();
  expect(screen.getByTestId("tool-row-summary").textContent).toBe('Completed "#4"');
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows).toHaveLength(1);
  expect(rows[0]!.getAttribute("data-touch")).toBe("done");
  expect(rows[0]!.textContent).toContain("#4");
});

test("appending N tasks without raw renders one fallback row per newly appended task", () => {
  renderItem(
    taskItem(
      {
        action: "append",
        tasks: [
          { type: "implement", description: "build the thing" },
          { type: "verify", description: "check the thing" },
        ],
      },
      "Added 2 task(s). Progress: 0/2 tasks complete.",
    ),
  );
  openRow();
  expect(screen.getByTestId("task-card")).toBeTruthy();
  const rows = screen.getAllByTestId("task-card-row");
  expect(rows).toHaveLength(2);
  expect(within(rows[0]!).getByText("build the thing")).toBeTruthy();
  expect(within(rows[0]!).getByTestId("task-check")).toBeTruthy();
  expect(screen.getByText("check the thing")).toBeTruthy();
});

test("current add and update arguments without raw render argument-only mutation rows", () => {
  const { unmount } = renderItem(
    taskItem(
      { add: [{ type: "implement", description: "build the current thing" }] },
      "Added 1 task(s). Progress: 0/1 tasks complete.",
    ),
  );
  openRow();
  expect(screen.getByTestId("task-card-row").textContent).toContain("build the current thing");

  unmount();
  // Same-id item: reset the store so the second render starts folded (see
  // the window-shrinks test above).
  resetDisclosureStoreForTests();
  renderItem(
    taskItem(
      { update: [{ id: 3, status: "cancelled", notes: "no longer needed" }] },
      "Updated 3→cancelled. Progress: 0 done, 1 cancelled, 0 remaining (1 total).",
    ),
  );
  openRow();
  const row = screen.getByTestId("task-card-row");
  expect(row.getAttribute("data-touch")).toBe("cancelled");
  expect(row.textContent).toContain("no longer needed");
});
