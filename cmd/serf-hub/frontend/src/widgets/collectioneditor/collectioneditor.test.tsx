import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, test, vi } from "vitest";
import { type CollectionAddResult, CollectionEditor } from "./index";

afterEach(cleanup);

interface DirItem {
  path: string;
}

const ITEMS: DirItem[] = [{ path: "/opt/plugins" }, { path: "/home/user/.serf/plugins" }];

function baseProps(overrides: Partial<Parameters<typeof CollectionEditor<DirItem>>[0]> = {}) {
  return {
    label: "Plugin directories",
    items: ITEMS,
    getKey: (item: DirItem) => item.path,
    renderItem: (item: DirItem) => item.path,
    removeLabel: (item: DirItem) => `Remove ${item.path}`,
    onRemove: vi.fn(),
    emptyMessage: "No plugin directories. Add one below.",
    addPlaceholder: "/opt/plugins",
    onAdd: vi.fn(async (): Promise<CollectionAddResult> => ({ ok: true })),
    ...overrides,
  };
}

function ControlledCollectionEditor(props: {
  initial: DirItem[];
  onAdd: (value: string) => Promise<CollectionAddResult>;
  onRemove?: (item: DirItem) => void;
  renderAddField?: Parameters<typeof CollectionEditor<DirItem>>[0]["renderAddField"];
}) {
  const [items, setItems] = useState(props.initial);
  return (
    <CollectionEditor<DirItem>
      label="Plugin directories"
      items={items}
      getKey={(item) => item.path}
      renderItem={(item) => item.path}
      removeLabel={(item) => `Remove ${item.path}`}
      onRemove={(item) => {
        setItems((current) => current.filter((i) => i.path !== item.path));
        props.onRemove?.(item);
      }}
      emptyMessage="No plugin directories. Add one below."
      addPlaceholder="/opt/plugins"
      onAdd={async (value) => {
        const result = await props.onAdd(value);
        if (result.ok) setItems((current) => [...current, { path: value }]);
        return result;
      }}
      renderAddField={props.renderAddField}
    />
  );
}

test("renders the list with an accessible name", () => {
  render(<CollectionEditor<DirItem> {...baseProps()} />);
  expect(screen.getByRole("list", { name: "Plugin directories" })).toBeTruthy();
});

test("renders one row per item via renderItem", () => {
  render(<CollectionEditor<DirItem> {...baseProps()} />);
  expect(screen.getByText("/opt/plugins")).toBeTruthy();
  expect(screen.getByText("/home/user/.serf/plugins")).toBeTruthy();
});

test("renders the empty message and no rows when items is empty", () => {
  render(<CollectionEditor<DirItem> {...baseProps({ items: [] })} />);
  expect(screen.getByText("No plugin directories. Add one below.")).toBeTruthy();
  expect(screen.queryByText("/opt/plugins")).toBeNull();
});

test("each row has a remove button with the given accessible name", () => {
  render(<CollectionEditor<DirItem> {...baseProps()} />);
  expect(screen.getByRole("button", { name: "Remove /opt/plugins" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Remove /home/user/.serf/plugins" })).toBeTruthy();
});

test("clicking a row's remove button calls onRemove with that item immediately, no built-in confirm", async () => {
  const user = userEvent.setup();
  const onRemove = vi.fn();
  render(<CollectionEditor<DirItem> {...baseProps({ onRemove })} />);
  await user.click(screen.getByRole("button", { name: "Remove /opt/plugins" }));
  expect(onRemove).toHaveBeenCalledWith(ITEMS[0]);
});

test("the add button is disabled while the add field is blank", () => {
  render(<CollectionEditor<DirItem> {...baseProps()} />);
  expect((screen.getByRole("button", { name: "Add" }) as HTMLButtonElement).disabled).toBe(true);
});

test("typing non-blank text into the add field enables the add button", async () => {
  const user = userEvent.setup();
  render(<CollectionEditor<DirItem> {...baseProps()} />);
  await user.type(screen.getByPlaceholderText("/opt/plugins"), "/opt/more");
  expect((screen.getByRole("button", { name: "Add" }) as HTMLButtonElement).disabled).toBe(false);
});

test("whitespace-only text leaves the add button disabled", async () => {
  const user = userEvent.setup();
  render(<CollectionEditor<DirItem> {...baseProps()} />);
  await user.type(screen.getByPlaceholderText("/opt/plugins"), "   ");
  expect((screen.getByRole("button", { name: "Add" }) as HTMLButtonElement).disabled).toBe(true);
});

test("submitting (Enter) calls onAdd with the trimmed value", async () => {
  const user = userEvent.setup();
  const onAdd = vi.fn(async (): Promise<CollectionAddResult> => ({ ok: true }));
  render(<CollectionEditor<DirItem> {...baseProps({ onAdd })} />);
  await user.type(screen.getByPlaceholderText("/opt/plugins"), "  /opt/more  {Enter}");
  expect(onAdd).toHaveBeenCalledWith("/opt/more");
});

test("clicking the Add button calls onAdd the same way as Enter", async () => {
  const user = userEvent.setup();
  const onAdd = vi.fn(async (): Promise<CollectionAddResult> => ({ ok: true }));
  render(<CollectionEditor<DirItem> {...baseProps({ onAdd })} />);
  await user.type(screen.getByPlaceholderText("/opt/plugins"), "/opt/more");
  await user.click(screen.getByRole("button", { name: "Add" }));
  expect(onAdd).toHaveBeenCalledWith("/opt/more");
});

test("a successful add clears the input", async () => {
  const user = userEvent.setup();
  render(<ControlledCollectionEditor initial={[]} onAdd={async () => ({ ok: true })} />);
  const input = screen.getByPlaceholderText("/opt/plugins") as HTMLInputElement;
  await user.type(input, "/opt/more{Enter}");
  expect(await screen.findByText("/opt/more")).toBeTruthy();
  expect(input.value).toBe("");
});

test("a failed add keeps the input's value and shows the inline error, does not add a row", async () => {
  const user = userEvent.setup();
  const onAdd = vi.fn(async (): Promise<CollectionAddResult> => ({ ok: false, error: "Path does not exist." }));
  render(<ControlledCollectionEditor initial={[]} onAdd={onAdd} />);
  const input = screen.getByPlaceholderText("/opt/plugins") as HTMLInputElement;
  await user.type(input, "/nope{Enter}");

  expect(await screen.findByText("Path does not exist.")).toBeTruthy();
  expect(input.value).toBe("/nope");
  expect(screen.queryByText("/nope", { selector: "li *" })).toBeNull();
});

test("a subsequent successful add clears a previously shown error", async () => {
  const user = userEvent.setup();
  const onAdd = vi
    .fn<(value: string) => Promise<CollectionAddResult>>()
    .mockResolvedValueOnce({ ok: false, error: "Path does not exist." })
    .mockResolvedValueOnce({ ok: true });
  render(<ControlledCollectionEditor initial={[]} onAdd={onAdd} />);
  const input = screen.getByPlaceholderText("/opt/plugins") as HTMLInputElement;

  await user.type(input, "/nope{Enter}");
  expect(await screen.findByText("Path does not exist.")).toBeTruthy();

  await user.clear(input);
  await user.type(input, "/opt/good{Enter}");

  expect(await screen.findByText("/opt/good")).toBeTruthy();
  expect(screen.queryByText("Path does not exist.")).toBeNull();
});

test("the input and add button are disabled while onAdd is pending, and re-enabled after it settles", async () => {
  const user = userEvent.setup();
  let resolveAdd: (result: CollectionAddResult) => void = () => {};
  const onAdd = vi.fn(
    () =>
      new Promise<CollectionAddResult>((resolve) => {
        resolveAdd = resolve;
      }),
  );
  render(<CollectionEditor<DirItem> {...baseProps({ onAdd })} />);
  const input = screen.getByPlaceholderText("/opt/plugins") as HTMLInputElement;

  await user.type(input, "/opt/more{Enter}");
  expect(input.disabled).toBe(true);
  expect((screen.getByRole("button", { name: "Add" }) as HTMLButtonElement).disabled).toBe(true);

  resolveAdd({ ok: true });
  await waitFor(() => expect(input.disabled).toBe(false));
});

describe("renderAddField", () => {
  // A minimal stand-in for a caller-supplied custom add field (e.g.
  // dirListSetting.tsx's PathPicker, or collectionFields.tsx's two-box
  // envMap field) - a single labeled text input plus its own submit
  // button, wired to CollectionEditor's own {value, onChange, disabled}
  // rather than rendering the built-in plain Input.
  function customField({
    value,
    onChange,
    disabled,
  }: Parameters<NonNullable<Parameters<typeof CollectionEditor<DirItem>>[0]["renderAddField"]>>[0]) {
    return (
      <>
        <input
          aria-label="Custom path field"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          disabled={disabled}
          placeholder="custom placeholder"
        />
        <button type="submit">Add</button>
      </>
    );
  }

  test("renders the caller's custom add field instead of the built-in Input", () => {
    render(<CollectionEditor<DirItem> {...baseProps({ renderAddField: customField })} />);
    expect(screen.getByRole("textbox", { name: "Custom path field" })).toBeTruthy();
    expect(screen.queryByPlaceholderText("/opt/plugins")).toBeNull();
  });

  test("typing into the custom field and submitting calls onAdd with the trimmed value", async () => {
    const user = userEvent.setup();
    const onAdd = vi.fn(async (): Promise<CollectionAddResult> => ({ ok: true }));
    render(<CollectionEditor<DirItem> {...baseProps({ onAdd, renderAddField: customField })} />);
    await user.type(screen.getByRole("textbox", { name: "Custom path field" }), "  /opt/more  {Enter}");
    expect(onAdd).toHaveBeenCalledWith("/opt/more");
  });

  test("a successful add through the custom field clears its value (CollectionEditor still owns draft)", async () => {
    const user = userEvent.setup();
    render(<ControlledCollectionEditor initial={[]} onAdd={async () => ({ ok: true })} renderAddField={customField} />);
    const input = screen.getByRole("textbox", { name: "Custom path field" }) as HTMLInputElement;
    await user.type(input, "/opt/more{Enter}");
    expect(await screen.findByText("/opt/more")).toBeTruthy();
    expect(input.value).toBe("");
  });

  test("a failed add through the custom field shows the inline error below the form", async () => {
    const user = userEvent.setup();
    const onAdd = vi.fn(async (): Promise<CollectionAddResult> => ({ ok: false, error: "Path does not exist." }));
    render(<CollectionEditor<DirItem> {...baseProps({ onAdd, renderAddField: customField })} />);
    await user.type(screen.getByRole("textbox", { name: "Custom path field" }), "/nope{Enter}");
    expect(await screen.findByText("Path does not exist.")).toBeTruthy();
  });

  test("the custom field's disabled arg mirrors the built-in busy state while onAdd is pending", async () => {
    const user = userEvent.setup();
    let resolveAdd: (result: CollectionAddResult) => void = () => {};
    const onAdd = vi.fn(
      () =>
        new Promise<CollectionAddResult>((resolve) => {
          resolveAdd = resolve;
        }),
    );
    render(<CollectionEditor<DirItem> {...baseProps({ onAdd, renderAddField: customField })} />);
    const input = screen.getByRole("textbox", { name: "Custom path field" }) as HTMLInputElement;
    await user.type(input, "/opt/more{Enter}");
    expect(input.disabled).toBe(true);
    resolveAdd({ ok: true });
    await waitFor(() => expect(input.disabled).toBe(false));
  });

  test("with renderAddField given, CollectionEditor renders no default Add button of its own (the caller's field owns it)", () => {
    render(<CollectionEditor<DirItem> {...baseProps({ renderAddField: customField })} />);
    // customField above renders exactly one submit button named "Add" -
    // if CollectionEditor ALSO rendered its own default button, there would
    // be two.
    expect(screen.getAllByRole("button", { name: "Add" })).toHaveLength(1);
  });
});
