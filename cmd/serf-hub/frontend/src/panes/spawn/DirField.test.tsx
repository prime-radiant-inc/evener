import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { DirField } from "./DirField";

class MemoryStorage {
  private store = new Map<string, string>();
  get length(): number {
    return this.store.size;
  }
  key(index: number): string | null {
    return Array.from(this.store.keys())[index] ?? null;
  }
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  globalThis.localStorage = new MemoryStorage() as unknown as Storage;
});
beforeEach(() => localStorage.clear());
afterEach(() => cleanup());

interface Harness {
  onChange: ReturnType<typeof vi.fn>;
  complete: ReturnType<typeof vi.fn>;
  listRecents: ReturnType<typeof vi.fn>;
}

function renderField(overrides: Partial<Harness> = {}, value = ""): Harness {
  const onChange = overrides.onChange ?? vi.fn();
  const complete = overrides.complete ?? vi.fn().mockResolvedValue([]);
  const listRecents = overrides.listRecents ?? vi.fn().mockResolvedValue([]);
  render(
    <DirField
      value={value}
      onChange={onChange as (value: string) => void}
      listRecents={listRecents as () => Promise<string[]>}
      complete={complete as (prefix: string) => Promise<string[]>}
    />,
  );
  return { onChange, complete, listRecents };
}

test("typing keeps focus in the input while the completion panel is open", async () => {
  const user = userEvent.setup();
  const { onChange } = renderField();

  const input = screen.getByRole("textbox");
  await user.click(input);
  await user.type(input, "/tmp");

  // The completion popover opens on the first keystroke; it must NOT steal
  // focus from the input (combobox pattern), or every character after the
  // first is lost — the exact regression Spawn.test.tsx caught (cwd "/").
  expect(document.activeElement).toBe(input);
  expect(onChange).toHaveBeenCalledTimes(4);
});

test("the Browse button opens a popup listing recent projects", async () => {
  const user = userEvent.setup();
  renderField({ listRecents: vi.fn().mockResolvedValue(["/home/me/alpha", "/home/me/beta"]) });

  await user.click(screen.getByRole("button", { name: "Browse working directory" }));

  expect(await screen.findByText("Recent projects")).toBeTruthy();
  expect(screen.getByText("/home/me/alpha")).toBeTruthy();
  expect(screen.getByText("/home/me/beta")).toBeTruthy();
});

test("clicking a recent project accepts it immediately and closes the popup", async () => {
  const user = userEvent.setup();
  const { onChange } = renderField({ listRecents: vi.fn().mockResolvedValue(["/home/me/alpha"]) });

  await user.click(screen.getByRole("button", { name: "Browse working directory" }));
  await screen.findByText("Recent projects");
  await user.click(screen.getByText("/home/me/alpha"));

  expect(onChange).toHaveBeenCalledWith("/home/me/alpha");
  await waitFor(() => expect(screen.queryByText("Recent projects")).toBeNull());
  // Accepting persists the global last-working-dir for the next picker's seed.
  expect(localStorage.getItem("serf-hub.spawn-defaults.global.last-working-dir")).toBe("/home/me/alpha");
});

test("clicking a listed directory browses INTO it (lists its children) rather than accepting", async () => {
  const user = userEvent.setup();
  const complete = vi
    .fn()
    .mockResolvedValueOnce(["/root/proj"]) // initial children on open
    .mockResolvedValueOnce(["/root/proj/src", "/root/proj/docs"]); // after browsing into /root/proj
  const { onChange } = renderField({ complete }, "/root");

  await user.click(screen.getByRole("button", { name: "Browse working directory" }));
  await screen.findByRole("button", { name: /proj/ });
  await user.click(screen.getByRole("button", { name: /^proj/ }));

  // Browsed, not accepted: complete re-fired for the children, no commit.
  await waitFor(() => expect(complete).toHaveBeenLastCalledWith("/root/proj/"));
  expect(onChange).not.toHaveBeenCalled();
  expect(await screen.findByRole("button", { name: "src" })).toBeTruthy();
});

test("drops the recent-projects section after the first browse-into", async () => {
  const user = userEvent.setup();
  const complete = vi.fn().mockResolvedValueOnce(["/root/proj"]).mockResolvedValueOnce([]);
  renderField({ complete, listRecents: vi.fn().mockResolvedValue(["/home/me/alpha"]) }, "/root");

  await user.click(screen.getByRole("button", { name: "Browse working directory" }));
  await screen.findByText("Recent projects");
  await user.click(screen.getByRole("button", { name: /^proj/ }));

  await waitFor(() => expect(screen.queryByText("Recent projects")).toBeNull());
});

test("shows a `..` parent row when browsing a non-root directory and browses up on click", async () => {
  const user = userEvent.setup();
  const complete = vi.fn().mockResolvedValue([]);
  renderField({ complete }, "/home/me/proj");

  await user.click(screen.getByRole("button", { name: "Browse working directory" }));
  const parent = await screen.findByRole("button", { name: "../" });
  await user.click(parent);

  await waitFor(() => expect(complete).toHaveBeenLastCalledWith("/home/me/"));
});

test("debounces completion requests while typing (~150ms)", async () => {
  vi.useFakeTimers();
  try {
    const complete = vi.fn().mockResolvedValue([]);
    renderField({ complete });

    // fireEvent (not userEvent) here: userEvent's async input model does not
    // compose with fake timers without a real scheduler. One change event is
    // exactly one handleType, which schedules exactly one debounced completion.
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "/ho" } });
    expect(complete).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(150);
    expect(complete).toHaveBeenCalledTimes(1);
    expect(complete).toHaveBeenCalledWith("/ho");
  } finally {
    vi.useRealTimers();
  }
});

test("drops a stale (out-of-order) completion response", async () => {
  const user = userEvent.setup();
  let resolveFirst: (v: string[]) => void = () => {};
  let resolveSecond: (v: string[]) => void = () => {};
  const complete = vi
    .fn()
    .mockImplementationOnce(() => new Promise<string[]>((r) => (resolveFirst = r)))
    .mockImplementationOnce(() => new Promise<string[]>((r) => (resolveSecond = r)));
  renderField({ complete }, "/a");

  await user.click(screen.getByRole("button", { name: "Browse working directory" }));
  await waitFor(() => expect(complete).toHaveBeenCalledTimes(1));
  // Fire a second request (browse via typing) before the first resolves.
  const textbox = screen.getAllByRole("textbox")[0];
  if (!textbox) throw new Error("expected a textbox");
  await user.type(textbox, "x");
  await waitFor(() => expect(complete).toHaveBeenCalledTimes(2));

  // Resolve the SECOND (latest) first, then the stale FIRST.
  resolveSecond(["/a/fresh"]);
  await screen.findByRole("button", { name: "fresh" });
  resolveFirst(["/a/stale"]);

  // The stale response must never replace the fresh entries.
  await waitFor(() => expect(screen.queryByRole("button", { name: "stale" })).toBeNull());
  expect(screen.getByRole("button", { name: "fresh" })).toBeTruthy();
});

test("collapses to one text input (no separate path input or Use this directory button)", async () => {
  const user = userEvent.setup();
  renderField({ complete: vi.fn().mockResolvedValue([]) }, "");

  await user.click(screen.getByRole("button", { name: "Browse working directory" }));
  await screen.findByRole("button", { name: "Cancel" });

  // Exactly one text input in the field
  const inputs = screen.getAllByRole("textbox");
  expect(inputs).toHaveLength(1);
  // No "Use this directory" button
  expect(screen.queryByRole("button", { name: "Use this directory" })).toBeNull();
});

test("browse popover is portaled and never reflows", async () => {
  const user = userEvent.setup();
  renderField({ complete: vi.fn().mockResolvedValue([]) }, "");

  await user.click(screen.getByRole("button", { name: "Browse working directory" }));
  const cancelButton = await screen.findByRole("button", { name: "Cancel" });

  // The Cancel button (or any element in the browse list) should have document.body in its parent chain via portal
  let el: Element | null = cancelButton;
  let foundDocumentBody = false;
  while (el) {
    if (el === document.body) {
      foundDocumentBody = true;
      break;
    }
    el = el.parentElement;
  }
  expect(foundDocumentBody).toBe(true);
});
