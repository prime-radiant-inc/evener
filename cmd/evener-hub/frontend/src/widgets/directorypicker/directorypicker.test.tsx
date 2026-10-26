import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Profiler } from "react";
import { createPortal } from "react-dom";
import { expect, test, vi } from "vitest";
import { DirectoryIcon as FromBarrel } from "..";
import { appendPopout } from "../internal/popoutTestUtils";
import { DirectoryIcon, DirectoryPicker, type DirectoryPickerProps } from "./index";

function setup(overrides: Partial<DirectoryPickerProps> = {}, onCommit: () => void = () => {}) {
  const props: DirectoryPickerProps = {
    value: "/work",
    onClose: vi.fn(),
    onPick: vi.fn(),
    complete: async (prefix) => (prefix === "/work/" ? ["/work/app", "/work/other"] : []),
    listRecents: async () => ["/recent/app"],
    validatePath: async (path) => ({ valid: true, path: path === "~" ? "/home/test" : path }),
    createDirectory: async () => {},
    ...overrides,
  };
  const view = render(
    <Profiler id="picker" onRender={onCommit}>
      <DirectoryPicker {...props} />
    </Profiler>,
  );
  return { ...props, user: userEvent.setup(), unmount: view.unmount };
}

function setupPendingInitialValidation() {
  let resolveInitial: (result: { valid: boolean; path: string }) => void = () => {};
  const initial = new Promise<{ valid: boolean; path: string }>((resolve) => {
    resolveInitial = resolve;
  });
  const validatePath = vi
    .fn(async (path: string) => ({ valid: path !== "/missing", path: `/canonical${path}` }))
    .mockReturnValueOnce(initial);
  const complete = vi.fn(async (prefix: string) => [`${prefix}child`]);
  return {
    ...setup({ value: "~", validatePath, complete }),
    validatePath,
    complete,
    resolveInitial: () => resolveInitial({ valid: true, path: "/home/test" }),
  };
}

test("its folder glyph is exported from the widget barrel", () => {
  expect(FromBarrel).toBe(DirectoryIcon);
});

test("navigation and recent folders stay draft until explicitly selected", async () => {
  const { user, onPick } = setup();
  await user.click(await screen.findByRole("button", { name: "Open /work/app" }));
  expect(onPick).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Parent directory" }));
  await screen.findByRole("button", { name: "Open /work/other" });
  await user.click(screen.getByRole("button", { name: "Open recent /recent/app" }));
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  expect(onPick).toHaveBeenCalledWith("/recent/app");
});

test("recent rows carry the full path on hover after the sidebar truncates it", async () => {
  setup();
  const row = await screen.findByRole("button", { name: "Open recent /recent/app" });
  expect(row.title).toBe("/recent/app");
});

test("typed paths are validated and canonicalized before selection", async () => {
  const { user, onPick } = setup();
  const input = screen.getByRole("textbox", { name: "Path" });
  await user.clear(input);
  await user.type(input, "~{Enter}");
  await waitFor(() => expect((input as HTMLInputElement).value).toBe("/home/test"));
  expect(onPick).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  expect(onPick).toHaveBeenCalledWith("/home/test");
});

test("an invalid typed path cannot accidentally select the previously browsed folder", async () => {
  const validationDetail = "fixture-path-validation-detail";
  const { user, onPick } = setup({
    validatePath: async (path) => ({ valid: path !== "/missing", path, error: validationDetail }),
  });
  await screen.findByRole("button", { name: "Open /work/app" });
  const input = screen.getByRole("textbox", { name: "Path" });
  await user.clear(input);
  await user.type(input, "/missing{Enter}");
  expect((await screen.findByRole("alert")).textContent).toBe(validationDetail);
  expect((screen.getByRole("button", { name: "Use this folder" }) as HTMLButtonElement).disabled).toBe(true);
  expect(onPick).not.toHaveBeenCalled();
});

test("creating a folder navigates into it without starting or selecting a session", async () => {
  const createDirectory = vi.fn(async () => {});
  const { user, onPick } = setup({ createDirectory });
  await screen.findByRole("button", { name: "Open /work/app" });
  await user.click(screen.getByRole("button", { name: "New folder" }));
  await user.type(screen.getByRole("textbox", { name: "Folder name" }), "new project{Enter}");
  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Path" }) as HTMLInputElement).value).toBe("/work/new project"),
  );
  expect(createDirectory).toHaveBeenCalledExactlyOnceWith("/work/new project");
  expect(onPick).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  expect(onPick).toHaveBeenCalledWith("/work/new project");
});

test("creation failures preserve the folder name and allow retry", async () => {
  const createDirectory = vi.fn().mockRejectedValueOnce(new Error("permission denied")).mockResolvedValue(undefined);
  const { user } = setup({ createDirectory });
  await screen.findByRole("button", { name: "Open /work/app" });
  await user.click(screen.getByRole("button", { name: "New folder" }));
  await user.type(screen.getByRole("textbox", { name: "Folder name" }), "draft{Enter}");
  await screen.findByRole("alert");
  expect((screen.getByRole("textbox", { name: "Folder name" }) as HTMLInputElement).value).toBe("draft");
  await user.click(screen.getByRole("button", { name: "Create folder" }));
  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Path" }) as HTMLInputElement).value).toBe("/work/draft"),
  );
});

test.each(["create", "cancel"])("%s new folder keeps focus inside the dialog for Escape", async (action) => {
  const { user, onClose } = setup();
  await screen.findByRole("button", { name: "Open /work/app" });
  await user.click(screen.getByRole("button", { name: "New folder" }));
  if (action === "create") {
    await user.type(screen.getByRole("textbox", { name: "Folder name" }), "new{Enter}");
    await waitFor(() =>
      expect((screen.getByRole("textbox", { name: "Path" }) as HTMLInputElement).value).toBe("/work/new"),
    );
  } else {
    await user.click(screen.getByRole("button", { name: "Cancel new folder" }));
  }
  expect(screen.getByRole("dialog").contains(document.activeElement)).toBe(true);
  await user.keyboard("{Escape}");
  expect(onClose).toHaveBeenCalledOnce();
});

test("creation restores dialog focus before the completed form is painted", async () => {
  let finishCreate!: () => void;
  const focusedCommits: boolean[] = [];
  let sawNameInput = false;
  const { user } = setup(
    {
      createDirectory: () =>
        new Promise<void>((resolve) => {
          finishCreate = resolve;
        }),
    },
    () => {
      const dialog = document.querySelector('[role="dialog"]');
      const name = document.querySelector('input[autocomplete="off"]:not([aria-label="Path"])');
      if (name) sawNameInput = true;
      else if (sawNameInput && dialog) focusedCommits.push(dialog.contains(document.activeElement));
    },
  );
  await screen.findByRole("button", { name: "Open /work/app" });
  await user.click(screen.getByRole("button", { name: "New folder" }));
  await user.type(screen.getByRole("textbox", { name: "Folder name" }), "new{Enter}");
  await act(async () => finishCreate());
  expect(focusedCommits.length).toBeGreaterThan(0);
  expect(focusedCommits.every(Boolean)).toBe(true);
});

test("a late directory listing cannot replace a newer navigation", async () => {
  let resolveListing: (paths: string[]) => void = () => {};
  const oldListing = new Promise<string[]>((resolve) => {
    resolveListing = resolve;
  });
  const { user } = setup({
    complete: (prefix) => (prefix === "/work/" ? oldListing : Promise.resolve(["/recent/app/src"])),
  });
  await user.click(await screen.findByRole("button", { name: "Open recent /recent/app" }));
  const folders = screen.getByRole("region", { name: "Folders" });
  await within(folders).findByRole("button", { name: "Open /recent/app/src" });
  await act(async () => resolveListing(["/work/stale"]));
  expect(within(folders).queryByRole("button", { name: "Open /work/stale" })).toBeNull();
});

test("root and empty folders remain selectable", async () => {
  const { user, onPick } = setup({ value: "/", complete: async () => [] });
  await waitFor(() =>
    expect((screen.getByRole("button", { name: "Use this folder" }) as HTMLButtonElement).disabled).toBe(false),
  );
  expect((screen.getByRole("button", { name: "Parent directory" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  expect(onPick).toHaveBeenCalledWith("/");
});

test.each(["pending", "failed"])("a validated directory remains selectable when listing is %s", async (listing) => {
  const { user, onPick } = setup({
    complete: () => (listing === "failed" ? Promise.reject(new Error("listing unavailable")) : new Promise(() => {})),
  });
  if (listing === "failed") await screen.findByRole("alert");
  const confirm = screen.getByRole("button", { name: "Use this folder" });
  await waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false));
  await user.click(confirm);
  expect(onPick).toHaveBeenCalledWith("/work");
});

test("a created directory can be selected while its listing is pending", async () => {
  const { user, onPick } = setup({
    complete: (prefix) => (prefix === "/work/" ? Promise.resolve([]) : new Promise(() => {})),
  });
  const newFolder = screen.getByRole("button", { name: "New folder" });
  await waitFor(() => expect((newFolder as HTMLButtonElement).disabled).toBe(false));
  await user.click(newFolder);
  await user.type(screen.getByRole("textbox", { name: "Folder name" }), "new project{Enter}");
  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Path" }) as HTMLInputElement).value).toBe("/work/new project"),
  );
  const confirm = screen.getByRole("button", { name: "Use this folder" });
  await waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false));
  await user.click(confirm);
  expect(onPick).toHaveBeenCalledWith("/work/new project");
});

test("first path focus lets typing replace the initial directory", async () => {
  const { user } = setup();
  await screen.findByRole("button", { name: "Open /work/app" });
  const input = screen.getByRole("textbox", { name: "Path" });
  await user.click(input);
  await user.keyboard("/replacement");
  expect((input as HTMLInputElement).value).toBe("/replacement");
});

test.each(["pending", "loaded"])("editing a path clears a %s listing until Go", async (listing) => {
  let resolveListing: (paths: string[]) => void = () => {};
  const oldListing = new Promise<string[]>((resolve) => {
    resolveListing = resolve;
  });
  const complete = vi.fn((prefix: string) => (prefix === "/work/" ? oldListing : Promise.resolve(["/draft/child"])));
  const { user, onPick } = setup({ complete });
  await waitFor(() => expect(complete).toHaveBeenCalledWith("/work/", false));
  if (listing === "loaded") await act(async () => resolveListing(["/work/stale"]));
  const input = screen.getByRole("textbox", { name: "Path" });
  await user.clear(input);
  await user.type(input, "/draft");
  const folders = screen.getByRole("region", { name: "Folders" });
  expect(folders.getAttribute("aria-busy")).toBe("false");
  expect(within(folders).queryByRole("button")).toBeNull();
  if (listing === "pending") await act(async () => resolveListing(["/work/stale"]));
  expect(within(folders).queryByRole("button")).toBeNull();
  expect(complete).toHaveBeenCalledTimes(1);
  expect((input as HTMLInputElement).value).toBe("/draft");
  expect((screen.getByRole("button", { name: "Use this folder" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(screen.getByRole("button", { name: "Go" }));
  await within(folders).findByRole("button", { name: "Open /draft/child" });
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  expect(onPick).toHaveBeenCalledExactlyOnceWith("/draft");
});

test("typing before initial validation resolves preserves the unsubmitted path", async () => {
  let resolveValidation: (result: { valid: boolean; path: string }) => void = () => {};
  const initialValidation = new Promise<{ valid: boolean; path: string }>((resolve) => {
    resolveValidation = resolve;
  });
  const validatePath = vi.fn(async (path: string) => ({ valid: true, path })).mockReturnValueOnce(initialValidation);
  const { user, onPick } = setup({ validatePath });
  const input = screen.getByRole("textbox", { name: "Path" });
  await user.clear(input);
  await user.type(input, "/draft");
  await act(async () => resolveValidation({ valid: true, path: "/work" }));
  expect((input as HTMLInputElement).value).toBe("/draft");
  expect(screen.getByRole("region", { name: "Folders" }).getAttribute("aria-busy")).toBe("false");
  await user.click(screen.getByRole("button", { name: "Go" }));
  await waitFor(() =>
    expect((screen.getByRole("button", { name: "Use this folder" }) as HTMLButtonElement).disabled).toBe(false),
  );
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  expect(onPick).toHaveBeenCalledExactlyOnceWith("/draft");
});

test.each(["Enter", "Go"])(
  "initial validation preserves focused selection before replacement and %s",
  async (submit) => {
    const { user, validatePath, complete, resolveInitial, onPick } = setupPendingInitialValidation();
    const input = screen.getByRole<HTMLInputElement>("textbox", { name: "Path" });
    await user.click(input);
    expect([input.selectionStart, input.selectionEnd]).toEqual([0, 1]);
    await act(async () => resolveInitial());
    expect(input.value).toBe("~");
    expect(document.activeElement).toBe(input);
    expect([input.selectionStart, input.selectionEnd]).toEqual([0, 1]);
    expect(complete).toHaveBeenCalledExactlyOnceWith("/home/test/", false);
    await screen.findByRole("button", { name: "Open /home/test/child" });
    await user.keyboard("/replacement");
    expect(input.value).toBe("/replacement");
    if (submit === "Enter") await user.keyboard("{Enter}");
    else await user.click(screen.getByRole("button", { name: "Go" }));
    await waitFor(() => expect(input.value).toBe("/canonical/replacement"));
    expect(validatePath).toHaveBeenNthCalledWith(2, "/replacement", "dir");
    await user.click(screen.getByRole("button", { name: "Use this folder" }));
    expect(onPick).toHaveBeenCalledExactlyOnceWith("/canonical/replacement");
  },
);

test("focus taken while the initial validation result is waiting to render keeps its selection", async () => {
  const { resolveInitial } = setupPendingInitialValidation();
  const input = screen.getByRole<HTMLInputElement>("textbox", { name: "Path" });
  // React flushes the validation result on a later task. A user (or a browser
  // driver) can focus and select the field after the result was received and
  // before it renders; the render must not replace the text under that selection.
  const actEnvironment = (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = false;
  try {
    resolveInitial();
    for (let tick = 0; tick < 10; tick++) await Promise.resolve();
    input.focus();
    input.select();
    await new Promise((resolve) => setTimeout(resolve, 50));
  } finally {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = actEnvironment;
  }
  expect(input.value).toBe("~");
  expect([input.selectionStart, input.selectionEnd]).toEqual([0, 1]);
});

test.each(["confirm", "blur"])(
  "an untouched focused validated draft supports %s without revalidation",
  async (action) => {
    const { user, resolveInitial, validatePath, onPick } = setupPendingInitialValidation();
    const input = screen.getByRole<HTMLInputElement>("textbox", { name: "Path" });
    await user.click(input);
    await act(async () => resolveInitial());
    expect(input.value).toBe("~");
    const confirm = screen.getByRole<HTMLButtonElement>("button", { name: "Use this folder" });
    expect(confirm.disabled).toBe(false);
    if (action === "blur") {
      await user.tab();
      expect(input.value).toBe("/home/test");
      expect(onPick).not.toHaveBeenCalled();
    }
    await user.click(confirm);
    expect(onPick).toHaveBeenCalledExactlyOnceWith("/home/test");
    expect(validatePath).toHaveBeenCalledTimes(1);
  },
);

test("editing a preserved initial draft prevents normalization and selection of the old canonical directory", async () => {
  const { user, resolveInitial, validatePath, onPick } = setupPendingInitialValidation();
  const input = screen.getByRole<HTMLInputElement>("textbox", { name: "Path" });
  await user.click(input);
  await act(async () => resolveInitial());
  expect(input.value).toBe("~");
  await user.keyboard("/missing");
  await user.tab();
  expect(input.value).toBe("/missing");
  expect(validatePath).toHaveBeenCalledTimes(1);
  const confirm = screen.getByRole<HTMLButtonElement>("button", { name: "Use this folder" });
  expect(confirm.disabled).toBe(true);
  await user.click(screen.getByRole("button", { name: "Go" }));
  await screen.findByRole("alert");
  expect(validatePath).toHaveBeenNthCalledWith(2, "/missing", "dir");
  expect(input.value).toBe("/missing");
  expect(confirm.disabled).toBe(true);
  await user.click(confirm);
  expect(onPick).not.toHaveBeenCalled();
});

test("explicit recent navigation replaces a preserved initial draft with its canonical destination", async () => {
  const { user, resolveInitial, onPick } = setupPendingInitialValidation();
  const input = screen.getByRole<HTMLInputElement>("textbox", { name: "Path" });
  await user.click(input);
  await act(async () => resolveInitial());
  expect(input.value).toBe("~");
  await user.click(screen.getByRole("button", { name: "Open recent /recent/app" }));
  await waitFor(() => expect(input.value).toBe("/canonical/recent/app"));
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  expect(onPick).toHaveBeenCalledExactlyOnceWith("/canonical/recent/app");
});

test("late initial validation cannot replace a submitted focused draft", async () => {
  const { user, resolveInitial, complete } = setupPendingInitialValidation();
  const input = screen.getByRole<HTMLInputElement>("textbox", { name: "Path" });
  await user.click(input);
  await user.keyboard("/replacement{Enter}");
  await waitFor(() => expect(input.value).toBe("/canonical/replacement"));
  await act(async () => resolveInitial());
  expect(input.value).toBe("/canonical/replacement");
  expect(complete).toHaveBeenCalledExactlyOnceWith("/canonical/replacement/", false);
});

test("initial validation after closing a focused picker starts no listing or selection", async () => {
  const { user, resolveInitial, unmount, complete, onPick } = setupPendingInitialValidation();
  await user.click(screen.getByRole("textbox", { name: "Path" }));
  unmount();
  await act(async () => resolveInitial());
  expect(complete).not.toHaveBeenCalled();
  expect(onPick).not.toHaveBeenCalled();
});

// --- popout realm (Dockview) ---------------------------------------------
// A Dockview popout is a separate Window with its own Document. The picker's
// panel is its only DOM output, so the picker resolves the document it was
// mounted in and portals into THAT body, and its focus reads must ask that
// document what has focus.
function popoutProps(overrides: Partial<DirectoryPickerProps> = {}): DirectoryPickerProps {
  return {
    value: "/work",
    onClose: vi.fn(),
    onPick: vi.fn(),
    complete: async (prefix) => (prefix === "/work/" ? ["/work/app"] : []),
    listRecents: async () => [],
    validatePath: async (path) => ({ valid: true, path }),
    createDirectory: async () => {},
    ...overrides,
  };
}

test("a picker mounted in a popout portals its panel into the popout body and focuses there", () => {
  const { frame, doc } = appendPopout();
  const view = render(createPortal(<DirectoryPicker {...popoutProps()} />, doc.body));
  try {
    const dialog = doc.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect(doc.body.contains(dialog)).toBe(true);
    expect(document.body.contains(dialog)).toBe(false);
    // FocusScope moved focus in, in the popout's own document.
    expect(dialog?.contains(doc.activeElement)).toBe(true);
  } finally {
    view.unmount();
    frame.remove();
  }
});

test("navigating from a focused path input in a popout keeps focus in that input", async () => {
  const { frame, doc } = appendPopout();
  const view = render(createPortal(<DirectoryPicker {...popoutProps()} />, doc.body));
  try {
    const folder = await waitFor(() => {
      const button = doc.querySelector<HTMLButtonElement>('button[aria-label="Open /work/app"]');
      if (button === null) throw new Error("popout folder row missing");
      return button;
    });
    const input = doc.querySelector<HTMLInputElement>('input[aria-label="Path"]');
    if (input === null) throw new Error("popout path input missing");
    input.focus();
    expect(doc.activeElement).toBe(input);

    fireEvent.click(folder);

    // browse() keeps a focused path input focused: reading the opener's
    // document would see the input unfocused and steal focus to Go.
    await waitFor(() => expect(doc.activeElement).toBe(input));
  } finally {
    view.unmount();
    frame.remove();
  }
});

test("initial validation in a popout reads focus from that document and preserves the focused draft", async () => {
  const { frame, doc } = appendPopout();
  let resolveInitial: (result: { valid: boolean; path: string }) => void = () => {};
  const initial = new Promise<{ valid: boolean; path: string }>((resolve) => {
    resolveInitial = resolve;
  });
  const validatePath = vi
    .fn(async (path: string) => ({ valid: true, path: `/canonical${path}` }))
    .mockReturnValueOnce(initial);
  const complete = vi.fn(async (prefix: string) => [`${prefix}child`]);
  const view = render(
    createPortal(<DirectoryPicker {...popoutProps({ value: "~", validatePath, complete })} />, doc.body),
  );
  try {
    const input = doc.querySelector<HTMLInputElement>('input[aria-label="Path"]');
    if (input === null) throw new Error("popout path input missing");
    input.focus();
    expect(doc.activeElement).toBe(input);

    await act(async () => resolveInitial({ valid: true, path: "/home/test" }));
    await waitFor(() => expect(complete).toHaveBeenCalledWith("/home/test/", false));
    // A focused input keeps its unsubmitted draft; reading the opener's
    // document would see no focus there and collapse it to the canonical path.
    expect(input.value).toBe("~");
    expect(doc.activeElement).toBe(input);
  } finally {
    view.unmount();
    frame.remove();
  }
});
