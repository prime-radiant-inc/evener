import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type FormEvent, useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { PathPicker } from "./index";

afterEach(cleanup);

function ControlledPathPicker(props: {
  initial: string;
  listChildren: (path: string) => Promise<string[]>;
  onChange?: (value: string) => void;
}) {
  const [value, setValue] = useState(props.initial);
  return (
    <PathPicker
      id="dir"
      value={value}
      onChange={(next) => {
        setValue(next);
        props.onChange?.(next);
      }}
      listChildren={props.listChildren}
    />
  );
}

test("renders a text input carrying the given value", () => {
  render(<PathPicker id="dir" value="/opt/plugins" onChange={vi.fn()} listChildren={vi.fn(async () => [])} />);
  expect((screen.getByDisplayValue("/opt/plugins") as HTMLInputElement).id).toBe("dir");
});

test("typing reflects into the input on every keystroke, like a plain text input", async () => {
  const user = userEvent.setup();
  render(<ControlledPathPicker initial="" listChildren={vi.fn(async () => [])} />);
  await user.type(screen.getByRole("textbox"), "/opt");
  expect((screen.getByRole("textbox") as HTMLInputElement).value).toBe("/opt");
});

test("typing a non-empty value opens the popup and lists children of the typed value's directory", async () => {
  const user = userEvent.setup();
  const listChildren = vi.fn(async (path: string) => (path === "/opt" ? ["/opt/plugins", "/opt/other"] : []));
  render(<ControlledPathPicker initial="" listChildren={listChildren} />);

  await user.type(screen.getByRole("textbox"), "/opt/pl");

  await waitFor(() => expect(listChildren).toHaveBeenCalledWith("/opt"));
  expect(await screen.findByText("plugins")).toBeTruthy();
  // "other" does not match the typed prefix "pl" and is filtered out.
  expect(screen.queryByText("other")).toBeNull();
});

test("clicking a suggestion row browses into it without calling onChange, and keeps the popup open", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  const listChildren = vi.fn(async (path: string) => {
    if (path === "/opt") return ["/opt/plugins"];
    if (path === "/opt/plugins") return ["/opt/plugins/serf-lint"];
    return [];
  });
  render(<PathPicker id="dir" value="/opt" onChange={onChange} listChildren={listChildren} />);

  await user.click(screen.getByRole("button", { name: "Browse" }));
  const row = await screen.findByRole("button", { name: "plugins" });
  await user.click(row);

  expect(onChange).not.toHaveBeenCalled();
  expect(await screen.findByText("serf-lint")).toBeTruthy();
  expect(listChildren).toHaveBeenCalledWith("/opt/plugins");
});

test("Accept commits the currently browsed directory exactly once and closes the popup", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  const listChildren = vi.fn(async (path: string) => (path === "/opt" ? ["/opt/plugins"] : []));
  render(<PathPicker id="dir" value="/opt" onChange={onChange} listChildren={listChildren} />);

  await user.click(screen.getByRole("button", { name: "Browse" }));
  await user.click(await screen.findByRole("button", { name: "plugins" }));
  await user.click(screen.getByRole("button", { name: "Use this folder" }));

  expect(onChange).toHaveBeenCalledTimes(1);
  expect(onChange).toHaveBeenCalledWith("/opt/plugins");
  expect(screen.queryByRole("button", { name: "Use this folder" })).toBeNull();
});

test("the Browse button opens the popup listing children of the full current value, unfiltered", async () => {
  const user = userEvent.setup();
  const listChildren = vi.fn(async (path: string) => (path === "/opt" ? ["/opt/plugins", "/opt/skills"] : []));
  render(<PathPicker id="dir" value="/opt" onChange={vi.fn()} listChildren={listChildren} />);

  await user.click(screen.getByRole("button", { name: "Browse" }));

  await waitFor(() => expect(listChildren).toHaveBeenCalledWith("/opt"));
  expect(await screen.findByText("plugins")).toBeTruthy();
  expect(screen.getByText("skills")).toBeTruthy();
});

test("ArrowDown opens the popup when it isn't already open", async () => {
  const user = userEvent.setup();
  const listChildren = vi.fn(async () => ["/opt/plugins"]);
  render(<PathPicker id="dir" value="/opt" onChange={vi.fn()} listChildren={listChildren} />);

  screen.getByRole("textbox").focus();
  await user.keyboard("{ArrowDown}");

  expect(await screen.findByRole("button", { name: "Use this folder" })).toBeTruthy();
});

test("Enter accepts the currently browsed directory and prevents a default form submit", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  const onSubmit = vi.fn((event: FormEvent) => event.preventDefault());
  const listChildren = vi.fn(async () => ["/opt/plugins"]);
  render(
    <form onSubmit={onSubmit}>
      <PathPicker id="dir" value="/opt" onChange={onChange} listChildren={listChildren} />
    </form>,
  );

  await user.click(screen.getByRole("button", { name: "Browse" }));
  await screen.findByRole("button", { name: "Use this folder" });
  screen.getByRole("textbox").focus();
  await user.keyboard("{Enter}");

  expect(onChange).toHaveBeenCalledWith("/opt");
  expect(onSubmit).not.toHaveBeenCalled();
  expect(screen.queryByRole("button", { name: "Use this folder" })).toBeNull();
});

test("Escape closes the popup without calling onChange", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<PathPicker id="dir" value="/opt" onChange={onChange} listChildren={vi.fn(async () => ["/opt/plugins"])} />);

  await user.click(screen.getByRole("button", { name: "Browse" }));
  await screen.findByRole("button", { name: "Use this folder" });
  await user.keyboard("{Escape}");

  expect(screen.queryByRole("button", { name: "Use this folder" })).toBeNull();
  expect(onChange).not.toHaveBeenCalled();
});

test("clicking outside the widget closes the popup without calling onChange", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(
    <div>
      <PathPicker id="dir" value="/opt" onChange={onChange} listChildren={vi.fn(async () => ["/opt/plugins"])} />
      <button type="button">elsewhere</button>
    </div>,
  );

  await user.click(screen.getByRole("button", { name: "Browse" }));
  await screen.findByRole("button", { name: "Use this folder" });
  await user.click(screen.getByRole("button", { name: "elsewhere" }));

  expect(screen.queryByRole("button", { name: "Use this folder" })).toBeNull();
  expect(onChange).not.toHaveBeenCalled();
});

test("a listChildren rejection is treated as an empty, non-crashing result", async () => {
  const user = userEvent.setup();
  const listChildren = vi.fn(async () => {
    throw new Error("permission denied");
  });
  render(<PathPicker id="dir" value="/opt" onChange={vi.fn()} listChildren={listChildren} />);

  await user.click(screen.getByRole("button", { name: "Browse" }));

  expect(await screen.findByText(/couldn't load/i)).toBeTruthy();
});

test("disabled disables both the input and the Browse button", () => {
  render(<PathPicker id="dir" value="/opt" onChange={vi.fn()} listChildren={vi.fn(async () => [])} disabled />);
  expect((screen.getByRole("textbox") as HTMLInputElement).disabled).toBe(true);
  expect((screen.getByRole("button", { name: "Browse" }) as HTMLButtonElement).disabled).toBe(true);
});

test("a custom browseLabel replaces the default Browse accessible name", () => {
  render(
    <PathPicker
      id="dir"
      value="/opt"
      onChange={vi.fn()}
      listChildren={vi.fn(async () => [])}
      browseLabel="Browse for a plugin directory"
    />,
  );
  expect(screen.getByRole("button", { name: "Browse for a plugin directory" })).toBeTruthy();
});

test("declares a :focus-visible rule in its CSS module, using only tokens", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "pathpicker.module.css"), "utf8");
  expect(css).toContain(":focus-visible");
});
