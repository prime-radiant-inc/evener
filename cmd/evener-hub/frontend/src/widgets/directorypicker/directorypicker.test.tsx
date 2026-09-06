import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Profiler } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { DirectoryPicker, type DirectoryPickerProps } from "./index";

afterEach(cleanup);

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
  render(
    <Profiler id="picker" onRender={onCommit}>
      <DirectoryPicker {...props} />
    </Profiler>,
  );
  return { ...props, user: userEvent.setup() };
}

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
