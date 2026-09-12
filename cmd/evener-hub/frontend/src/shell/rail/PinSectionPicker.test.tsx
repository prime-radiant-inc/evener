import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { WireError } from "../../protocol/errors";
import type { NavigationSessionSummary } from "../../protocol/types.gen";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { keyID, type ResourceState } from "../../stores/navigation/types";
import sheetStyles from "../../widgets/sheet/sheet.module.css";
import type { PinSectionSummary } from "./actions";
import { PinSectionPicker, type PinSectionPickerProps } from "./PinSectionPicker";

// PinSectionPicker reads pin sections from the navigation store's bounded
// pin-catalog resource (loadPinCatalogPages + selectPinSections). Each test seeds the store
// with a pin_catalog resource and stubs loadPinCatalogPages so the picker's mount
// effect resolves without a real network fetch.
const generation = "generation_test";
const pinKey = { kind: "pin_catalog" as const, offset: 0, limit: 100 };

function session(overrides: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary {
  return {
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

function pinCatalogResource(sections: PinSectionSummary[], remaining = 0): ResourceState {
  return {
    key: pinKey,
    data: {
      generation_id: generation,
      revision: 1,
      pin_sections: sections.map((s) => ({ id: s.id, name: s.name, count: s.member_count })),
      remaining,
    },
    loadedRevision: 1,
    targetRevision: 1,
    forceToken: 0,
    etag: "a",
    loading: false,
    stale: false,
    error: null,
    generationID: generation,
  };
}

function seedPinCatalog(sections: PinSectionSummary[], remaining = 0): ResourceState {
  const resource = pinCatalogResource(sections, remaining);
  navigationStore.setState({
    mode: "v2",
    resources: new Map([[keyID(resource.key), resource]]),
  });
  return resource;
}

// Replaces loadPinCatalogPages on the store state so it resolves without a network fetch. Returns a
// vi.fn so tests can assert call counts and override per-call behavior.
type LoadPinCatalogPages = (force?: boolean) => Promise<void>;
function stubLoadPinCatalog(): ReturnType<typeof vi.fn<LoadPinCatalogPages>> {
  const fn = vi.fn(async () => undefined) as ReturnType<typeof vi.fn<LoadPinCatalogPages>>;
  navigationStore.setState({ loadPinCatalogPages: fn as LoadPinCatalogPages });
  return fn;
}

afterEach(() => {
  cleanup();
  resetNavigationStoreForTests();
});

beforeEach(() => {
  vi.clearAllMocks();
  resetNavigationStoreForTests();
  seedPinCatalog([RESEARCH, CLIENT, PERSONAL]);
  stubLoadPinCatalog();
});

describe("PinSectionPicker", () => {
  test("fetches summaries on every mount and announces loading", async () => {
    const fn = navigationStore.getState().loadPinCatalogPages as ReturnType<typeof vi.fn>;
    let resolveFirst!: () => void;
    fn.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          resolveFirst = resolve;
        }),
    );

    const first = render(<PinSectionPicker {...props()} />);
    expect(screen.getByRole("status").textContent).toMatch(/loading sections/i);
    expect(fn).toHaveBeenCalledTimes(1);
    // Re-seed with just [CLIENT] so the resolved catalog shows one section.
    seedPinCatalog([CLIENT]);
    resolveFirst();
    await screen.findByRole("button", { name: "Client" });
    first.unmount();

    render(<PinSectionPicker {...props()} />);
    await screen.findByRole("button", { name: "Client" });
    expect(fn).toHaveBeenCalledTimes(2);
  });

  test("lists hidden empty sections in deterministic case-insensitive alphabetical order", async () => {
    seedPinCatalog([
      { id: "z", name: "research", member_count: 0 },
      { id: "b", name: "Client", member_count: 3 },
      { id: "a", name: "client", member_count: 0 },
      { id: "p", name: "Personal", member_count: 1 },
    ]);
    stubLoadPinCatalog();
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
    const fn = navigationStore.getState().loadPinCatalogPages as ReturnType<typeof vi.fn<LoadPinCatalogPages>>;
    const notFound = new WireError("pin section not found", -32602, { evenerErrorInfo: "resourceNotFound" });
    const onAssign = vi.fn().mockRejectedValue(notFound);
    render(<PinSectionPicker {...props({ onAssign })} />);

    // Wait for the mount-effect loadPinCatalog to resolve so "Client"
    // appears, then arm the 404-refresh call to stay pending until we've
    // re-seeded the store without "client".
    const clientButton = await screen.findByRole("button", { name: "Client" });
    let resolveRefresh!: () => void;
    fn.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          resolveRefresh = resolve;
        }),
    );

    await userEvent.setup().click(clientButton);

    expect((await screen.findByRole("alert")).textContent).toBe("pin section not found");
    // Re-seed the store without "client" so the refreshed catalog reflects
    // the deletion, then release the pending loadPinCatalog call.
    seedPinCatalog([PERSONAL]);
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
    resolveRefresh();
    await waitFor(() => expect(screen.queryByRole("button", { name: "Client" })).toBeNull());
    expect(screen.getByRole("button", { name: "Personal" })).toBeTruthy();
    expect(fn).toHaveBeenLastCalledWith(true);
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
