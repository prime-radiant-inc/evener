import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { PathField, type PathFieldProps } from "./index";

afterEach(() => cleanup());

/** A completion stub driven by a prefix -> entries table; an unlisted prefix
 * lists nothing, which is what the hub itself returns for a path it can't
 * read. */
function lister(table: Record<string, string[]>) {
  return vi.fn((prefix: string) => Promise.resolve(table[prefix] ?? []));
}

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

function renderField(props: Partial<PathFieldProps> = {}) {
  const onChange = props.onChange ?? vi.fn();
  const complete = props.complete ?? lister({});
  render(
    <PathField
      value={props.value ?? ""}
      onChange={onChange}
      kind={props.kind}
      complete={complete}
      listRecents={props.listRecents}
      disabled={props.disabled}
    />,
  );
  return { onChange, complete };
}

function trigger(): HTMLElement {
  return screen.getByRole("button", { name: /browse/i });
}

function panelInput(): HTMLInputElement | null {
  return screen.queryByRole("combobox", { name: "Path" }) as HTMLInputElement | null;
}

async function open(user: ReturnType<typeof userEvent.setup>): Promise<HTMLInputElement> {
  await user.click(trigger());
  return (await screen.findByRole("combobox", { name: "Path" })) as HTMLInputElement;
}

// --- closed state -----------------------------------------------------------

test("the closed field shows the value as plain text, and the default marker when empty", () => {
  const { unmount } = render(<PathField value="/home/jesse" onChange={vi.fn()} complete={lister({})} />);
  expect(screen.getByText("/home/jesse")).toBeTruthy();
  unmount();

  render(<PathField value="" onChange={vi.fn()} complete={lister({})} />);
  expect(screen.getByText("(default)")).toBeTruthy();
});

test("disabled disables the trigger, so the panel can't be opened", () => {
  renderField({ value: "/home/jesse", disabled: true });
  expect((trigger() as HTMLButtonElement).disabled).toBe(true);
});

// --- the input is pre-filled and replaced by the first keystroke ------------

test("opens pre-filled with the value, focused and FULLY selected", async () => {
  const user = userEvent.setup();
  renderField({ value: "/home/jesse", complete: lister({ "/home/jesse/": ["/home/jesse/src"] }) });

  const input = await open(user);

  await waitFor(() => expect(document.activeElement).toBe(input));
  expect(input.value).toBe("/home/jesse");
  expect(input.selectionStart).toBe(0);
  expect(input.selectionEnd).toBe("/home/jesse".length);
});

test("a dir field opens listing the value's own children, files excluded", async () => {
  const user = userEvent.setup();
  const { complete } = renderField({
    value: "/home/jesse",
    complete: lister({ "/home/jesse/": ["/home/jesse/src"] }),
  });

  await open(user);

  await waitFor(() => expect(screen.getByRole("option", { name: /src/ })).toBeTruthy());
  expect(complete).toHaveBeenCalledWith("/home/jesse/", false);
});

test("a file field opens listing its parent directory, files included", async () => {
  const user = userEvent.setup();
  const { complete } = renderField({
    kind: "file",
    value: "/etc/hosts",
    complete: lister({ "/etc/": ["/etc/ssl/", "/etc/hosts"] }),
  });

  await open(user);

  await waitFor(() => expect(screen.getByRole("option", { name: /hosts/ })).toBeTruthy());
  expect(complete).toHaveBeenCalledWith("/etc/", true);
});

// --- clicking rows: the value tracks the browse position --------------------

test("clicking a directory row writes it into the value AND lists its children, panel open", async () => {
  const user = userEvent.setup();
  const { onChange, complete } = renderField({
    value: "/home/jesse",
    complete: lister({
      "/home/jesse/": ["/home/jesse/src"],
      "/home/jesse/src/": ["/home/jesse/src/serf"],
    }),
  });

  await open(user);
  await user.click(await screen.findByRole("option", { name: /src/ }));

  expect(onChange).toHaveBeenCalledWith("/home/jesse/src");
  await waitFor(() => expect(complete).toHaveBeenCalledWith("/home/jesse/src/", false));
  expect(panelInput()).not.toBeNull();
  expect(await screen.findByRole("option", { name: /serf/ })).toBeTruthy();
});

test("clicking the ../ row browses to the parent, panel open", async () => {
  const user = userEvent.setup();
  const { onChange, complete } = renderField({
    value: "/home/jesse",
    complete: lister({ "/home/jesse/": ["/home/jesse/src"], "/home/": ["/home/jesse"] }),
  });

  await open(user);
  await user.click(await screen.findByRole("option", { name: "../" }));

  expect(onChange).toHaveBeenCalledWith("/home");
  await waitFor(() => expect(complete).toHaveBeenCalledWith("/home/", false));
  expect(panelInput()).not.toBeNull();
});

test("clicking a file row commits it and closes the panel", async () => {
  const user = userEvent.setup();
  const { onChange } = renderField({
    kind: "file",
    value: "/etc/hosts",
    complete: lister({ "/etc/": ["/etc/ssl/", "/etc/passwd"] }),
  });

  await open(user);
  await user.click(await screen.findByRole("option", { name: /passwd/ }));

  expect(onChange).toHaveBeenCalledWith("/etc/passwd");
  await waitFor(() => expect(panelInput()).toBeNull());
});

// --- Recent projects --------------------------------------------------------

test("the Recent group leads the list and disappears after the first keystroke", async () => {
  const user = userEvent.setup();
  renderField({
    value: "/home/jesse",
    complete: lister({ "/home/jesse/": ["/home/jesse/src"] }),
    listRecents: vi.fn().mockResolvedValue(["/home/jesse/serf"]),
  });

  await open(user);
  expect(await screen.findByText("Recent projects")).toBeTruthy();
  const options = screen.getAllByRole("option");
  expect(options[0]?.textContent).toMatch(/serf/);

  await user.keyboard("x");

  await waitFor(() => expect(screen.queryByText("Recent projects")).toBeNull());
});

test("clicking a recent row commits it and closes, unlike a directory row", async () => {
  const user = userEvent.setup();
  const { onChange } = renderField({
    value: "/home/jesse",
    complete: lister({ "/home/jesse/": ["/home/jesse/src"] }),
    listRecents: vi.fn().mockResolvedValue(["/home/jesse/serf"]),
  });

  await open(user);
  await user.click(await screen.findByRole("option", { name: /serf/ }));

  expect(onChange).toHaveBeenLastCalledWith("/home/jesse/serf");
  await waitFor(() => expect(panelInput()).toBeNull());
});

test("a rejected listRecents degrades silently to no Recent group", async () => {
  const user = userEvent.setup();
  renderField({
    value: "/home/jesse",
    complete: lister({ "/home/jesse/": ["/home/jesse/src"] }),
    listRecents: vi.fn().mockRejectedValue(new Error("no such method")),
  });

  await open(user);

  await waitFor(() => expect(screen.getByRole("option", { name: /src/ })).toBeTruthy());
  expect(screen.queryByText("Recent projects")).toBeNull();
});

// --- typing -----------------------------------------------------------------

test("Enter with nothing active commits the typed literal path", async () => {
  const user = userEvent.setup();
  const { onChange } = renderField({ kind: "outputFile", value: "", complete: lister({}) });

  await open(user);
  // user.keyboard, never user.type: typing must land on the already-focused
  // input rather than clicking it first (a click collapses the pre-fill's
  // selection).
  await user.keyboard("/tmp/atif.json{Enter}");

  expect(onChange).toHaveBeenLastCalledWith("/tmp/atif.json");
  await waitFor(() => expect(panelInput()).toBeNull());
});

test("typing filters the listing by the last path component", async () => {
  const user = userEvent.setup();
  renderField({
    value: "/home/jesse",
    complete: lister({ "/home/jesse/": ["/home/jesse/src", "/home/jesse/tmp"] }),
  });

  await open(user);
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3)); // ../ + two dirs
  await user.keyboard("/home/jesse/sr");

  await waitFor(() => expect(screen.getAllByRole("option", { name: /src|tmp/ })).toHaveLength(1));
});

// Typing past a "/" and straight on into the next component happens inside
// one debounce window: the keystrokes after the slash must not cancel the
// listing the slash asked for, or a fast typist gets the previous directory's
// entries filtered by a name that isn't in it.
test("keystrokes that only narrow the last component don't cancel the pending listing", async () => {
  const user = userEvent.setup();
  const { complete } = renderField({
    value: "/home/jesse",
    complete: lister({ "/home/jesse/": ["/home/jesse/src"], "/var/": ["/var/log"] }),
  });

  await open(user);
  await user.keyboard("/var/lo");

  await waitFor(() => expect(complete).toHaveBeenCalledWith("/var/", false));
  expect(await screen.findByRole("option", { name: /log/ })).toBeTruthy();
});

// A stale response overwriting a fresher one is the defect the monotonic
// request id exists to prevent.
test("a completion resolving AFTER a newer one never overwrites the newer entries", async () => {
  const user = userEvent.setup();
  const slow = deferred<string[]>();
  const complete = vi.fn((prefix: string) =>
    prefix === "/home/jesse/" ? slow.promise : Promise.resolve(["/var/log"]),
  );
  renderField({ value: "/home/jesse", complete });

  await open(user);
  await user.keyboard("/var/");
  expect(await screen.findByRole("option", { name: /log/ })).toBeTruthy();

  slow.resolve(["/home/jesse/stale"]);
  await waitFor(() => expect(complete).toHaveBeenCalledTimes(2));

  expect(screen.queryByRole("option", { name: /stale/ })).toBeNull();
  expect(screen.getByRole("option", { name: /log/ })).toBeTruthy();
});

test("a rejected complete renders the empty status line rather than throwing", async () => {
  const user = userEvent.setup();
  renderField({ value: "/home/jesse", complete: vi.fn().mockRejectedValue(new Error("permission denied")) });

  await open(user);

  expect(await screen.findByText("Nothing here.")).toBeTruthy();
});

// --- keyboard ---------------------------------------------------------------

test("ArrowDown/ArrowUp/Home/End walk the pickable rows without moving DOM focus", async () => {
  const user = userEvent.setup();
  renderField({
    value: "/home/jesse",
    complete: lister({ "/home/jesse/": ["/home/jesse/src", "/home/jesse/tmp"] }),
  });

  const input = await open(user);
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  const activeText = () => {
    const id = input.getAttribute("aria-activedescendant");
    return id ? document.getElementById(id)?.textContent : null;
  };

  await user.keyboard("{ArrowDown}");
  expect(activeText()).toBe("../");
  await user.keyboard("{End}");
  expect(activeText()).toMatch(/tmp/);
  await user.keyboard("{ArrowUp}");
  expect(activeText()).toMatch(/src/);
  await user.keyboard("{Home}");
  expect(activeText()).toBe("../");
  expect(document.activeElement).toBe(input);
});

test("Enter on an active directory row descends, exactly like clicking it", async () => {
  const user = userEvent.setup();
  const { onChange, complete } = renderField({
    value: "/home/jesse",
    complete: lister({ "/home/jesse/": ["/home/jesse/src"], "/home/jesse/src/": [] }),
  });

  await open(user);
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
  await user.keyboard("{End}{Enter}");

  expect(onChange).toHaveBeenLastCalledWith("/home/jesse/src");
  await waitFor(() => expect(complete).toHaveBeenCalledWith("/home/jesse/src/", false));
  expect(panelInput()).not.toBeNull();
});

// --- dismissal --------------------------------------------------------------

// closeOnScroll={false}: the panel's own list scrolls, and a page scroll
// behind it must not dismiss mid-interaction.
test("a window scroll does NOT dismiss the open panel", async () => {
  const user = userEvent.setup();
  renderField({ value: "/home/jesse", complete: lister({ "/home/jesse/": ["/home/jesse/src"] }) });

  await open(user);
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
  // Dispatched inside act so a close, if the opt-out were missing, would have
  // actually flushed to the DOM by the assertions below - a bare dispatch
  // asserts before React commits and passes either way.
  await act(async () => {
    window.dispatchEvent(new Event("scroll"));
  });

  expect(panelInput()).not.toBeNull();
  expect(screen.getAllByRole("option")).toHaveLength(2);
});

// Popover's FocusScope is opted out of focus management (autoFocus={false}) so
// the panel input can own focus AND its selection - which makes returning
// focus to the trigger on close this widget's own job. Without it focus falls
// to <body> and a keyboard user is stranded.
test("closing returns focus to the trigger", async () => {
  const user = userEvent.setup();
  renderField({ value: "/home/jesse", complete: lister({ "/home/jesse/": ["/home/jesse/src"] }) });
  const button = trigger();

  await open(user);
  await user.keyboard("{Escape}");

  await waitFor(() => expect(panelInput()).toBeNull());
  expect(document.activeElement).toBe(button);
});

test("Escape keeps whatever browsing left in the field - there is no Cancel", async () => {
  const user = userEvent.setup();
  const { onChange } = renderField({
    value: "/home/jesse",
    complete: lister({ "/home/jesse/": ["/home/jesse/src"], "/home/jesse/src/": [] }),
  });

  await open(user);
  await user.click(await screen.findByRole("option", { name: /src/ }));
  await user.keyboard("{Escape}");

  await waitFor(() => expect(panelInput()).toBeNull());
  expect(onChange).toHaveBeenLastCalledWith("/home/jesse/src");
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
});
