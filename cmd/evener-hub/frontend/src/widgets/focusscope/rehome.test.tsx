// Focus recovery's two halves, away from the surfaces that use them.
// rehomeFocus is the action: hand focus to the root's first control, and only
// when the browser left it on <body>. useFocusRehome is the judgement: re-home
// only when a control it remembered inside the root has just been removed - a
// fresh mount, a surviving control, or focus that moved elsewhere must all leave
// the keyboard exactly where it is (roborev round 7: keying on <body> alone moved
// focus into panes nobody was using).
import { act, cleanup, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { afterEach, expect, test } from "vitest";
import { rehomeFocus, useFocusRehome } from "./rehome";

afterEach(() => {
  cleanup();
  document.body.replaceChildren();
  (document.activeElement as HTMLElement | null)?.blur();
});

function rootWith(...controls: HTMLElement[]): HTMLElement {
  const root = document.createElement("div");
  root.append(...controls);
  document.body.append(root);
  return root;
}

test("rehomeFocus hands a focus dropped to <body> to the root's first tabbable control", () => {
  const button = document.createElement("button");
  const root = rootWith(button);
  expect(document.activeElement).toBe(document.body);

  rehomeFocus(root);
  expect(document.activeElement).toBe(button);
});

test("rehomeFocus leaves focus that is not on <body> exactly where it is", () => {
  const first = document.createElement("button");
  const second = document.createElement("button");
  const root = rootWith(first, second);
  second.focus();
  expect(document.activeElement).toBe(second);

  rehomeFocus(root);
  expect(document.activeElement).toBe(second);
});

test("rehomeFocus with nothing tabbable leaves focus where the browser put it", () => {
  const root = rootWith();
  rehomeFocus(root);
  expect(document.activeElement).toBe(document.body);
});

test("rehomeFocus without a root is a no-op", () => {
  expect(() => rehomeFocus(null)).not.toThrow();
  expect(document.activeElement).toBe(document.body);
});

/** A surface whose content can disappear under the keyboard: `row` is the
 * control that can be unmounted, `fallback` stands in for the surface's first
 * tabbable control. */
function Harness({ row }: { row: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useFocusRehome(ref);
  return (
    <div ref={ref}>
      {row && <button type="button">row</button>}
      <button type="button">fallback</button>
    </div>
  );
}

test("useFocusRehome leaves a fresh mount's focus alone", () => {
  render(<Harness row={false} />);
  expect(document.activeElement).toBe(document.body);
});

test("useFocusRehome hands focus to the root's first control when the remembered one is removed", () => {
  const { rerender } = render(<Harness row />);
  const row = screen.getByRole("button", { name: "row" });
  act(() => row.focus());
  expect(document.activeElement).toBe(row);

  rerender(<Harness row={false} />);
  expect(screen.queryByRole("button", { name: "row" })).toBeNull();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "fallback" }));
});

test("useFocusRehome leaves focus alone when the remembered control survives the commit", () => {
  const { rerender } = render(<Harness row />);
  const row = screen.getByRole("button", { name: "row" });
  act(() => row.focus());

  rerender(<Harness row />);
  expect(document.activeElement).toBe(row);
});

test("useFocusRehome leaves focus alone when focus is outside its root", () => {
  const outside = document.createElement("button");
  document.body.append(outside);
  const { rerender } = render(<Harness row />);
  act(() => outside.focus());

  rerender(<Harness row={false} />);
  expect(document.activeElement).toBe(outside);
});

/** A surface whose ref-holding node is REPLACED, the way the connect dialog's
 * body is: the dialog returns a different tree while an editor is open, so the
 * body div is unmounted and a new one mounted when the editor closes. The keys
 * make React do the same here (without them it reuses the node and the case
 * disappears). */
function SwapHarness({ editor, row }: { editor: boolean; row: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useFocusRehome(ref);
  return editor ? (
    <div key="editor" ref={ref}>
      <button type="button">editor</button>
    </div>
  ) : (
    <div key="rows" ref={ref}>
      {row && <button type="button">row</button>}
      <button type="button">fallback</button>
    </div>
  );
}

test("useFocusRehome keeps remembering focus after the root's node is replaced", () => {
  const { rerender } = render(<SwapHarness editor row={false} />);
  // The node the ref points at is swapped for a new one; the listener is bound
  // once (to the document), so it must resolve the node at event time.
  rerender(<SwapHarness editor={false} row />);
  const row = screen.getByRole("button", { name: "row" });
  act(() => row.focus());

  rerender(<SwapHarness editor={false} row={false} />);
  expect(screen.queryByRole("button", { name: "row" })).toBeNull();
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "fallback" }));
});

/** A surface whose focused control can be natively disabled (the instance
 * sheet's pending "Test credentials") or refusaed with aria-disabled. */
function DisableHarness({ state }: { state: "enabled" | "disabled" | "aria-disabled" }) {
  const ref = useRef<HTMLDivElement>(null);
  useFocusRehome(ref);
  return (
    <div ref={ref}>
      {state === "disabled" ? (
        <button type="button" disabled>
          row
        </button>
      ) : state === "aria-disabled" ? (
        <button type="button" aria-disabled="true">
          row
        </button>
      ) : (
        <button type="button">row</button>
      )}
      <button type="button">fallback</button>
    </div>
  );
}

test("useFocusRehome hands focus back when the remembered control becomes natively disabled", () => {
  const { rerender } = render(<DisableHarness state="enabled" />);
  const row = screen.getByRole("button", { name: "row" });
  act(() => row.focus());

  rerender(<DisableHarness state="disabled" />);
  expect((screen.getByRole("button", { name: "row" }) as HTMLButtonElement).disabled).toBe(true);
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "fallback" }));
});

test("useFocusRehome leaves an aria-disabled control holding focus", () => {
  const { rerender } = render(<DisableHarness state="enabled" />);
  const row = screen.getByRole("button", { name: "row" });
  act(() => row.focus());

  rerender(<DisableHarness state="aria-disabled" />);
  expect(document.activeElement).toBe(screen.getByRole("button", { name: "row" }));
});

test("useFocusRehome re-homes once per removal, not on every later commit", () => {
  const { rerender } = render(<Harness row />);
  const row = screen.getByRole("button", { name: "row" });
  act(() => row.focus());

  rerender(<Harness row={false} />);
  const fallback = screen.getByRole("button", { name: "fallback" });
  expect(document.activeElement).toBe(fallback);

  // The user moves on; a later commit must not drag focus back.
  const outside = document.createElement("button");
  document.body.append(outside);
  act(() => outside.focus());
  rerender(<Harness row={false} />);
  expect(document.activeElement).toBe(outside);
});
