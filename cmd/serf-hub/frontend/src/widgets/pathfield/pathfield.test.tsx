import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { PathField, type PathFieldProps } from "./index";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

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

// "" means "no value, let the hub default to home"; "/" is a real directory.
// Stripping the trailing slash off "/" collapses the two, and the field opens on
// home while the panel confidently labels it "Home".
test("a field holding / opens on the filesystem root, not on home", async () => {
  const user = userEvent.setup();
  const { complete } = renderField({ value: "/", complete: lister({ "/": ["/etc", "/var"] }) });

  await open(user);

  await waitFor(() => expect(complete).toHaveBeenCalledWith("/", false));
  expect(await screen.findByRole("option", { name: /etc/ })).toBeTruthy();
  // Scoped to the list: the closed trigger renders the same "/" text.
  const header = screen.getByRole("listbox").firstElementChild;
  expect(header?.textContent).toBe("/");
  expect(screen.queryByText("Home")).toBeNull();
  // Root has no parent to climb to.
  expect(screen.queryByRole("option", { name: "../" })).toBeNull();
});

test("typing / lists the filesystem root", async () => {
  const user = userEvent.setup();
  const { complete } = renderField({
    value: "/home/jesse",
    complete: lister({ "/home/jesse/": ["/home/jesse/src"], "/": ["/etc"] }),
  });

  await open(user);
  await user.keyboard("/");

  await waitFor(() => expect(complete).toHaveBeenCalledWith("/", false));
  expect(await screen.findByRole("option", { name: /etc/ })).toBeTruthy();
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

// The hub hides dotfiles unless the FILTER itself starts with a dot
// (app_paths.go: `HasPrefix(name, ".") && !HasPrefix(filter, ".")`), so
// "the directory didn't change" is not enough to skip a request: the entries
// in hand were fetched with a dotless filter and cannot contain a single
// dotted name, whatever the client-side narrowing does with them.
test("typing a leading dot re-lists, so dotfiles the hub hides by default are reachable", async () => {
  const user = userEvent.setup();
  const { complete } = renderField({
    value: "/home/jesse",
    complete: lister({
      "/home/jesse/": ["/home/jesse/src"],
      "/home/jesse/.": ["/home/jesse/.config"],
    }),
  });

  await open(user);
  await waitFor(() => expect(screen.getByRole("option", { name: /src/ })).toBeTruthy());
  await user.keyboard("/home/jesse/.");

  // The typed text itself is the prefix, filter and all - the hub splits it
  // into listDir + filter, and only the filter tells it to show dotfiles.
  await waitFor(() => expect(complete).toHaveBeenCalledWith("/home/jesse/.", false));
  expect(await screen.findByRole("option", { name: /\.config/ })).toBeTruthy();
});

// Deleting back to a dotless filter must re-list too: the dotted response the
// hub sent is ONLY dotfiles, so the plain listing has to be fetched again.
test("deleting the leading dot re-lists the plain directory", async () => {
  const user = userEvent.setup();
  const { complete } = renderField({
    value: "/home/jesse",
    complete: lister({
      "/home/jesse/": ["/home/jesse/src"],
      "/home/jesse/.": ["/home/jesse/.config"],
    }),
  });

  await open(user);
  await user.keyboard("/home/jesse/.");
  expect(await screen.findByRole("option", { name: /\.config/ })).toBeTruthy();

  await user.keyboard("{Backspace}");

  await waitFor(() => expect(complete).toHaveBeenLastCalledWith("/home/jesse/", false));
  expect(await screen.findByRole("option", { name: /src/ })).toBeTruthy();
});

// The header and the "../" row describe currentDir, which moves the instant a
// new directory is typed, while the listing only arrives 150ms later. Rendering
// the OLD directory's children under the NEW directory's header for that whole
// window makes the panel lie: clicking a row navigates somewhere the user never
// pointed at.
test("a newly typed directory clears the stale listing immediately, before the debounce fires", async () => {
  const complete = lister({
    "/home/jesse/": ["/home/jesse/src"],
    "/var/s": ["/var/spool"],
  });
  renderField({ value: "/home/jesse", complete });
  fireEvent.click(trigger());
  const input = (await screen.findByRole("combobox", { name: "Path" })) as HTMLInputElement;
  await waitFor(() => expect(screen.getByRole("option", { name: /src/ })).toBeTruthy());

  // Synchronous: no awaited tick, so the debounced request has NOT fired yet.
  fireEvent.change(input, { target: { value: "/var/s" } });

  expect(screen.getByText("/var")).toBeTruthy();
  expect(screen.queryByRole("option", { name: /src/ })).toBeNull();
  expect(screen.getByText("Loading…")).toBeTruthy();

  // And the real listing still lands once the window closes.
  expect(await screen.findByRole("option", { name: /spool/ })).toBeTruthy();
});

// The dot can be typed and then narrowed inside ONE debounce window, so the
// dot-ness comparison has to be against the pending request's filter rather
// than the last one that actually fired - otherwise ".c" is judged against the
// dotless listing on screen, matches nothing new, and the request never goes.
test("a dot typed and narrowed inside one debounce window still sends the dotted prefix", async () => {
  const complete = lister({
    "/home/jesse/": ["/home/jesse/src"],
    "/home/jesse/.co": ["/home/jesse/.config"],
  });
  renderField({ value: "/home/jesse", complete });
  fireEvent.click(trigger());
  const input = (await screen.findByRole("combobox", { name: "Path" })) as HTMLInputElement;
  await waitFor(() => expect(complete).toHaveBeenCalledWith("/home/jesse/", false));

  // fireEvent, not userEvent: two change events with no awaited delay between
  // them, so both land inside the single 150ms window.
  fireEvent.change(input, { target: { value: "/home/jesse/." } });
  fireEvent.change(input, { target: { value: "/home/jesse/.co" } });

  await waitFor(() => expect(complete).toHaveBeenCalledWith("/home/jesse/.co", false));
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

/** An open dir field whose recents are still in flight, so resolving them is a
 * late async arrival that re-renders the list under the user's feet. */
async function openWithLateRecents(user: ReturnType<typeof userEvent.setup>) {
  const recents = deferred<string[]>();
  const props = renderField({
    value: "/home/jesse",
    complete: lister({ "/home/jesse/": ["/home/jesse/src"], "/home/jesse/src/": [] }),
    listRecents: () => recents.promise,
  });
  const input = await open(user);
  await waitFor(() => expect(screen.getByRole("option", { name: /src/ })).toBeTruthy());
  return { ...props, input, resolveRecents: () => act(async () => recents.resolve(["/home/jesse/serf"])) };
}

// A listing that resolves after the user has already moved the highlight must
// not move it: seeding the current file's row is an OPENING courtesy, not a
// standing assertion.
test("a late listRecents leaves a user-moved highlight exactly where it was", async () => {
  const user = userEvent.setup();
  const { input, resolveRecents } = await openWithLateRecents(user);

  await user.keyboard("{End}");
  const highlighted = input.getAttribute("aria-activedescendant");
  expect(document.getElementById(highlighted ?? "")?.textContent).toMatch(/src/);

  await resolveRecents();

  expect(screen.getByText("Recent projects")).toBeTruthy();
  expect(input.getAttribute("aria-activedescendant")).toBe(highlighted);
});

// The consequence of losing the highlight is worse than cosmetic: Enter falls
// through to the commit-the-typed-literal branch and CLOSES the panel, which
// violates spec 3.6 (Enter on an active row does what clicking it does).
test("Enter after a late listRecents still descends into the highlighted directory", async () => {
  const user = userEvent.setup();
  const { input, onChange, complete, resolveRecents } = await openWithLateRecents(user);

  await user.keyboard("{End}");
  await resolveRecents();
  await user.keyboard("{Enter}");

  expect(onChange).toHaveBeenLastCalledWith("/home/jesse/src");
  await waitFor(() => expect(complete).toHaveBeenCalledWith("/home/jesse/src/", false));
  expect(panelInput()).not.toBeNull();
  expect(input.isConnected).toBe(true);
});

// aria-activedescendant naming an element that isn't in the DOM is an ARIA
// violation, and a listing being replaced can retire the highlighted row.
test("aria-activedescendant is dropped when the highlighted row leaves the listing", async () => {
  const user = userEvent.setup();
  const complete = lister({
    "/home/jesse/": ["/home/jesse/src", "/home/jesse/tmp"],
    "/home/jesse/z": [],
  });
  renderField({ value: "/home/jesse", complete });
  const input = await open(user);
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));

  await user.keyboard("{End}");
  expect(input.getAttribute("aria-activedescendant")).toBeTruthy();

  // A filter that matches nothing: the highlighted row is gone.
  await user.keyboard("/home/jesse/z");

  await waitFor(() => expect(screen.getByText("Nothing here.")).toBeTruthy());
  expect(input.getAttribute("aria-activedescendant")).toBeNull();
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
