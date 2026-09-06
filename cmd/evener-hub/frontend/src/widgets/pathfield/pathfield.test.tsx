import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { PathField, type PathFieldProps } from "./index";

afterEach(cleanup);

const directory = {
  validatePath: async (path: string) => ({ valid: path !== "/missing", path: path === "~" ? "/home" : path }),
  createDirectory: async () => {},
};

function setup(overrides: Partial<PathFieldProps> = {}) {
  const onChange = vi.fn();
  const onPanelClose = vi.fn();
  const complete = vi.fn(async () => [] as string[]);
  function Field() {
    const [value, setValue] = useState(overrides.value ?? "/work");
    return (
      <PathField
        directory={directory}
        complete={complete}
        onPanelClose={onPanelClose}
        {...overrides}
        value={value}
        onChange={(path) => {
          onChange(path);
          setValue(path);
        }}
      />
    );
  }
  render(<Field />);
  return { user: userEvent.setup(), onChange, onPanelClose, complete };
}

function trigger() {
  return screen.getByRole("button", { name: /browse/i });
}

async function open(user: ReturnType<typeof userEvent.setup>) {
  await user.click(trigger());
  return (await screen.findByLabelText("Path", { selector: "input" })) as HTMLInputElement;
}

test("directory trigger exposes its label, path and disabled state", () => {
  setup({ ariaLabel: "Skill directories", disabled: true });
  expect(
    (screen.getByRole("button", { name: "Skill directories: /work — browse" }) as HTMLButtonElement).disabled,
  ).toBe(true);
});

test("directory navigation stays draft until confirmation and restores trigger focus", async () => {
  const { user, onChange, onPanelClose } = setup({ complete: async () => ["/work/child"] });
  const fieldTrigger = trigger();
  expect(fieldTrigger.getAttribute("aria-haspopup")).toBe("dialog");
  expect(fieldTrigger.getAttribute("aria-expanded")).toBe("false");
  await open(user);
  expect(fieldTrigger.getAttribute("aria-expanded")).toBe("true");
  await user.click(await screen.findByRole("button", { name: "Open /work/child" }));
  expect(onChange).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  expect(onChange).toHaveBeenCalledExactlyOnceWith("/work/child");
  expect(onPanelClose).toHaveBeenCalledExactlyOnceWith("/work/child");
  await waitFor(() => expect(document.activeElement).toBe(trigger()));
  expect(fieldTrigger.getAttribute("aria-expanded")).toBe("false");
});

test.each(["Cancel", "Escape"])("%s preserves a directory field's committed value", async (action) => {
  const { user, onChange, onPanelClose } = setup();
  const input = await open(user);
  await user.clear(input);
  await user.type(input, "/draft{Enter}");
  if (action === "Cancel") await user.click(screen.getByRole("button", { name: "Cancel" }));
  else await user.keyboard("{Escape}");
  expect(onChange).not.toHaveBeenCalled();
  expect(onPanelClose).toHaveBeenCalledExactlyOnceWith("/work");
  expect(trigger().textContent).toContain("/work");
});

test("an empty directory field browses its fallback and excludes files", async () => {
  const { user, complete } = setup({ value: "", fallbackDir: "/fallback" });
  await open(user);
  await waitFor(() => expect(complete).toHaveBeenCalledWith("/fallback/", false));
  expect(screen.queryByText("Recent")).toBeNull();
});

test("directory creation uses injected actions and selects only after confirmation", async () => {
  const createDirectory = vi.fn(async () => {});
  const { user, onChange } = setup({ directory: { ...directory, createDirectory } });
  await open(user);
  const create = screen.getByRole("button", { name: "New folder" });
  await waitFor(() => expect((create as HTMLButtonElement).disabled).toBe(false));
  await user.click(create);
  await user.type(screen.getByRole("textbox", { name: "Folder name" }), "new{Enter}");
  await waitFor(() => expect(createDirectory).toHaveBeenCalledExactlyOnceWith("/work/new"));
  expect(onChange).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  expect(onChange).toHaveBeenCalledExactlyOnceWith("/work/new");
});

test("file selection lists the parent, commits a file once and restores focus", async () => {
  const complete = vi.fn(async () => ["/etc/ssl/", "/etc/passwd"]);
  const { user, onChange, onPanelClose } = setup({ kind: "file", value: "/etc/hosts", complete });
  await open(user);
  await user.click(await screen.findByRole("option", { name: /passwd/ }));
  expect(complete).toHaveBeenCalledWith("/etc/", true);
  expect(onChange).toHaveBeenLastCalledWith("/etc/passwd");
  expect(onPanelClose).toHaveBeenCalledExactlyOnceWith("/etc/passwd");
  await waitFor(() => expect(document.activeElement).toBe(trigger()));
});

test.each(["file", "outputFile"] as const)("%s accepts a typed literal path", async (kind) => {
  const { user, onPanelClose } = setup({ kind, value: "/etc/file" });
  const input = await open(user);
  expect(input.selectionStart).toBe(0);
  expect(input.selectionEnd).toBe(input.value.length);
  await user.keyboard("/tmp/new-file{Enter}");
  expect(onPanelClose).toHaveBeenCalledExactlyOnceWith("/tmp/new-file");
});

test("file browsing retains directory keyboard navigation", async () => {
  const complete = vi.fn(async (prefix: string) => (prefix === "/etc/" ? ["/etc/ssl/"] : ["/etc/ssl/cert.pem"]));
  const { user } = setup({ kind: "file", value: "/etc/file", complete });
  await open(user);
  await screen.findByRole("option", { name: /ssl/ });
  await user.keyboard("{End}{Enter}");
  await screen.findByRole("option", { name: /cert.pem/ });
  expect(complete).toHaveBeenCalledWith("/etc/ssl/", true);
});

test("file typing filters entries and relists for dotfiles", async () => {
  const complete = vi.fn(async (prefix: string) =>
    prefix.endsWith("/.") ? ["/etc/.secret"] : ["/etc/hosts", "/etc/passwd"],
  );
  const { user } = setup({ kind: "file", value: "/etc/file", complete });
  const input = await open(user);
  await screen.findByRole("option", { name: /passwd/ });
  await user.clear(input);
  await user.type(input, "/etc/ho");
  await screen.findByRole("option", { name: /hosts/ });
  expect(screen.queryByRole("option", { name: /passwd/ })).toBeNull();
  await user.clear(input);
  await user.type(input, "/etc/.");
  await screen.findByRole("option", { name: /.secret/ });
});

test("file listing failures are visible", async () => {
  const { user } = setup({
    kind: "file",
    complete: async () => {
      throw new Error("unavailable");
    },
  });
  await open(user);
  await screen.findByText("Something went wrong.");
  expect(screen.queryByText("Nothing here.")).toBeNull();
});

test("late file listings cannot replace newer navigation", async () => {
  let resolveOld: (paths: string[]) => void = () => {};
  const old = new Promise<string[]>((resolve) => {
    resolveOld = resolve;
  });
  const { user } = setup({
    kind: "file",
    value: "/old/file",
    complete: async (prefix) => (prefix === "/old/" ? old : ["/new/current"]),
  });
  const input = await open(user);
  await user.clear(input);
  await user.type(input, "/new/");
  await screen.findByRole("option", { name: /current/ });
  await act(async () => resolveOld(["/old/stale"]));
  expect(screen.queryByRole("option", { name: /stale/ })).toBeNull();
});

test("directory navigation and creation never submit the enclosing form", async () => {
  const submitted = vi.fn();
  const user = userEvent.setup();
  render(
    <form
      onSubmit={(event) => {
        event.preventDefault();
        submitted();
      }}
    >
      <PathField value="/work" onChange={vi.fn()} complete={async () => []} directory={directory} />
    </form>,
  );
  const input = await open(user);
  await user.clear(input);
  await user.type(input, "/other{Enter}");
  await user.click(screen.getByRole("button", { name: "New folder" }));
  await user.type(screen.getByRole("textbox", { name: "Folder name" }), "child{Enter}");
  expect(submitted).not.toHaveBeenCalled();
});

test("file typing past a slash keeps the directory listing within one debounce window", async () => {
  const complete = vi.fn(async (prefix: string) => (prefix === "/var/" ? ["/var/log.txt"] : []));
  const { user } = setup({ kind: "file", value: "/home/file", complete });
  await open(user);
  await user.keyboard("/var/lo");
  await waitFor(() => expect(complete).toHaveBeenCalledWith("/var/", true));
  await screen.findByRole("option", { name: /log.txt/ });
});

test("file dot-prefix typing and narrowing inside one debounce window still relists", async () => {
  const complete = vi.fn(async (prefix: string) => (prefix === "/home/.co" ? ["/home/.config"] : []));
  const { user } = setup({ kind: "file", value: "/home/file", complete });
  const input = await open(user);
  await waitFor(() => expect(complete).toHaveBeenCalledWith("/home/", true));
  fireEvent.change(input, { target: { value: "/home/." } });
  fireEvent.change(input, { target: { value: "/home/.co" } });
  await waitFor(() => expect(complete).toHaveBeenCalledWith("/home/.co", true));
  await screen.findByRole("option", { name: /.config/ });
});
