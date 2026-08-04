import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { PinSectionSummary, TreeNode } from "../../stores/tree";
import sheetStyles from "../../widgets/sheet/sheet.module.css";
import { listPinSections, RailRequestError } from "./actions";
import { PinSectionPicker, type PinSectionPickerProps } from "./PinSectionPicker";

vi.mock("./actions", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./actions")>();
  return { ...actual, listPinSections: vi.fn() };
});

const mockedListPinSections = vi.mocked(listPinSections);

function session(overrides: Partial<TreeNode> = {}): TreeNode {
  return {
    row_id: "project:p1:local:s1",
    ref: "local:s1",
    host_id: "local",
    session_id: "s1",
    title: "Session one",
    project: "Project",
    state: "idle",
    kind: "session",
    live: false,
    children: [],
    ...overrides,
  };
}

const CLIENT: PinSectionSummary = { id: "client", name: "Client", member_count: 0 };
const PERSONAL: PinSectionSummary = { id: "personal", name: "Personal", member_count: 2 };
const RESEARCH: PinSectionSummary = { id: "research", name: "research", member_count: 1 };

function props(overrides: Partial<PinSectionPickerProps> = {}): PinSectionPickerProps {
  return {
    session: session(),
    onAssign: vi.fn().mockResolvedValue(undefined),
    onClose: vi.fn(),
    ...overrides,
  };
}

afterEach(cleanup);

beforeEach(() => {
  vi.clearAllMocks();
  mockedListPinSections.mockResolvedValue([RESEARCH, CLIENT, PERSONAL]);
});

describe("PinSectionPicker", () => {
  test("fetches summaries on every mount and announces loading", async () => {
    let resolveFirst!: (sections: PinSectionSummary[]) => void;
    mockedListPinSections.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveFirst = resolve;
        }),
    );

    const first = render(<PinSectionPicker {...props()} />);
    expect(screen.getByRole("status").textContent).toMatch(/loading sections/i);
    expect(mockedListPinSections).toHaveBeenCalledTimes(1);
    resolveFirst([CLIENT]);
    await screen.findByRole("button", { name: "Client" });
    first.unmount();

    render(<PinSectionPicker {...props()} />);
    await screen.findByRole("button", { name: "Client" });
    expect(mockedListPinSections).toHaveBeenCalledTimes(2);
  });

  test("lists hidden empty sections in deterministic case-insensitive alphabetical order", async () => {
    mockedListPinSections.mockResolvedValue([
      { id: "z", name: "research", member_count: 0 },
      { id: "b", name: "Client", member_count: 3 },
      { id: "a", name: "client", member_count: 0 },
      { id: "p", name: "Personal", member_count: 1 },
    ]);
    render(<PinSectionPicker {...props()} />);

    const list = await screen.findByRole("list", { name: /pin sections/i });
    const names = Array.from(list.querySelectorAll("button")).map((button) => button.textContent?.trim());
    expect(names).toEqual(["client", "Client", "Personal", "research", "New section…"]);
  });

  test("assigns to an existing section with its fetched canonical summary", async () => {
    const onAssign = vi.fn().mockResolvedValue(undefined);
    render(<PinSectionPicker {...props({ onAssign })} />);

    await userEvent.setup().click(await screen.findByRole("button", { name: "Client" }));
    expect(onAssign).toHaveBeenCalledWith({ section_id: "client" }, CLIENT);
  });

  test("refreshes stale section choices when assigning a concurrently deleted section returns not-found", async () => {
    mockedListPinSections.mockResolvedValueOnce([CLIENT, PERSONAL]).mockResolvedValueOnce([PERSONAL]);
    const notFound = new RailRequestError("pin section not found", 404);
    const onAssign = vi.fn().mockRejectedValue(notFound);
    render(<PinSectionPicker {...props({ onAssign })} />);

    await userEvent.setup().click(await screen.findByRole("button", { name: "Client" }));

    expect((await screen.findByRole("alert")).textContent).toBe("pin section not found");
    await waitFor(() => expect(mockedListPinSections).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole("button", { name: "Client" })).toBeNull();
    expect(screen.getByRole("button", { name: "Personal" })).toBeTruthy();
  });

  test("opens the new-section step, trims the name, and assigns by name", async () => {
    const onAssign = vi.fn().mockResolvedValue(undefined);
    render(<PinSectionPicker {...props({ onAssign })} />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "New section…" }));
    await user.type(screen.getByRole("textbox", { name: "Section name" }), "  Research  ");
    await user.click(screen.getByRole("button", { name: "Create and pin" }));

    expect(onAssign).toHaveBeenCalledWith({ section_name: "Research" });
  });

  test("preserves entered text and shows the server error inline after rejection", async () => {
    const onAssign = vi.fn().mockRejectedValue(new Error("section name already exists"));
    render(<PinSectionPicker {...props({ onAssign })} />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "New section…" }));
    const input = screen.getByRole("textbox", { name: "Section name" }) as HTMLInputElement;
    await user.type(input, "Research");
    await user.click(screen.getByRole("button", { name: "Create and pin" }));

    expect((await screen.findByRole("alert")).textContent).toBe("section name already exists");
    expect(input.value).toBe("Research");
  });

  test.each([
    ["only whitespace", "   ", "Section name is required"],
    ["81 Unicode code points", "界".repeat(81), "Section names must be 80 characters or fewer"],
  ])("rejects %s before assigning", async (_case, value, message) => {
    const onAssign = vi.fn().mockResolvedValue(undefined);
    render(<PinSectionPicker {...props({ onAssign })} />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole("button", { name: "New section…" }));
    await user.type(screen.getByRole("textbox", { name: "Section name" }), value);
    await user.click(screen.getByRole("button", { name: "Create and pin" }));

    expect(screen.getByRole("alert").textContent).toBe(message);
    expect(onAssign).not.toHaveBeenCalled();
  });

  test("Cancel closes and Dialog restores focus to the opener", async () => {
    function Harness() {
      const [open, setOpen] = useState(false);
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>
            Open picker
          </button>
          {open && <PinSectionPicker {...props({ onClose: () => setOpen(false) })} />}
        </>
      );
    }

    const { useState } = await import("react");
    render(<Harness />);
    const user = userEvent.setup();
    const opener = screen.getByRole("button", { name: "Open picker" });
    opener.focus();
    await user.click(opener);
    await screen.findByRole("dialog", { name: /pin session one/i });

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(document.activeElement).toBe(opener));
  });

  test("renders as a bottom Sheet on mobile, keeping the centered Dialog on desktop", async () => {
    // Desktop first: jsdom has no matchMedia at all, so useIsMobile is false.
    const desktop = render(<PinSectionPicker {...props()} />);
    await screen.findByRole("dialog", { name: /pin session one/i });
    expect(screen.getByRole("dialog").className.split(" ")).not.toContain(sheetStyles.bottom);
    desktop.unmount();

    vi.stubGlobal(
      "matchMedia",
      vi.fn((query: string) => ({
        matches: true,
        media: query,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    );
    try {
      render(<PinSectionPicker {...props()} />);
      await screen.findByRole("dialog", { name: /pin session one/i });
      expect(screen.getByRole("dialog").className.split(" ")).toContain(sheetStyles.bottom);
      expect(vi.mocked(window.matchMedia)).toHaveBeenCalledWith("(max-width: 899px)");
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
