import {
  cleanup,
  createEvent,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BottomBar, type BottomTab } from "./BottomBar";

afterEach(() => {
  cleanup();
});

const PANEL_ID = "evener-tab-panel";

const TABS: readonly BottomTab[] = [
  { id: "sessions", label: "Sessions", glyph: "▣" },
  { id: "new", label: "New", glyph: "＋" },
  { id: "settings", label: "Settings", glyph: "⚙" },
];

function tab(name: RegExp) {
  return screen.getByRole("tab", { name });
}

/** Assert that exactly one tab is selected and report which. */
function expectExactlyOneSelected(expectedId: string) {
  const all = screen.getAllByRole("tab");
  const selected = all.filter(
    (n) => n.getAttribute("aria-selected") === "true",
  );
  expect(selected).toHaveLength(1);
  expect(selected[0]).toHaveAttribute("id", `${PANEL_ID}-tab-${expectedId}`);
  for (const t of TABS) {
    const node = tab(new RegExp(t.label, "i"));
    expect(node).toHaveAttribute("aria-selected", String(t.id === expectedId));
  }
}

/** Controlled wrapper mirroring RootShell: onSelect updates activeId. */
function ControlledBottomBar({
  initial = "sessions",
  onSelectSpy,
}: {
  initial?: string;
  onSelectSpy?: (id: string) => void;
} = {}) {
  const [activeId, setActiveId] = useState(initial);
  return (
    <BottomBar
      tabs={TABS}
      activeId={activeId}
      panelId={PANEL_ID}
      onSelect={(id) => {
        onSelectSpy?.(id);
        setActiveId(id);
      }}
    />
  );
}

describe("BottomBar — landmarks and roles", () => {
  it("renders a nav landmark with a child tablist and three tabs", () => {
    render(
      <BottomBar
        tabs={TABS}
        activeId="sessions"
        onSelect={() => {}}
        panelId={PANEL_ID}
      />,
    );
    const nav = screen.getByRole("navigation", { name: /primary/i });
    expect(nav).toBeInTheDocument();
    const tablist = within(nav).getByRole("tablist");
    expect(tablist).toBeInTheDocument();
    expect(screen.getAllByRole("tab").length).toBe(3);
  });

  it("each tab has role=tab, aria-selected, aria-controls, and a stable id", () => {
    render(
      <BottomBar
        tabs={TABS}
        activeId="new"
        onSelect={() => {}}
        panelId={PANEL_ID}
      />,
    );
    for (const t of TABS) {
      const node = tab(new RegExp(t.label, "i"));
      expect(node).toHaveAttribute("role", "tab");
      expect(node).toHaveAttribute("aria-controls", PANEL_ID);
      expect(node).toHaveAttribute("id", `${PANEL_ID}-tab-${t.id}`);
      expect(node).toHaveAttribute("aria-selected", String(t.id === "new"));
    }
  });

  it("glyph is hidden from assistive tech; the visible label is the accessible name", () => {
    render(
      <BottomBar
        tabs={TABS}
        activeId="sessions"
        onSelect={() => {}}
        panelId={PANEL_ID}
      />,
    );
    // Accessible name is the label only (glyph is aria-hidden).
    expect(tab(/sessions/i)).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: /▣/ })).toBeNull();
    // The visible label text is rendered.
    expect(screen.getByText("Sessions")).toBeInTheDocument();
    // The glyph element is marked hidden from AT.
    const glyph = screen.getByText("▣");
    expect(glyph).toHaveAttribute("aria-hidden", "true");
  });

  it("keeps the CSS-owned tap-target class hook unchanged", () => {
    render(
      <BottomBar
        tabs={TABS}
        activeId="sessions"
        onSelect={() => {}}
        panelId={PANEL_ID}
      />,
    );
    expect(tab(/sessions/i).className).toMatch(/evener-tab/);
  });

  it("does not introduce a disabled concept", () => {
    render(
      <BottomBar
        tabs={TABS}
        activeId="sessions"
        onSelect={() => {}}
        panelId={PANEL_ID}
      />,
    );
    for (const t of TABS) {
      const node = tab(new RegExp(t.label, "i"));
      expect(node).not.toHaveAttribute("disabled");
      expect(node).not.toHaveAttribute("aria-disabled");
    }
  });
});

describe("BottomBar — roving tabIndex", () => {
  it("active tab is tabbable (0), inactive tabs are -1", () => {
    render(
      <BottomBar
        tabs={TABS}
        activeId="new"
        onSelect={() => {}}
        panelId={PANEL_ID}
      />,
    );
    expect(tab(/new/i)).toHaveAttribute("tabindex", "0");
    expect(tab(/sessions/i)).toHaveAttribute("tabindex", "-1");
    expect(tab(/settings/i)).toHaveAttribute("tabindex", "-1");
  });

  it("controlled activeId update makes exactly that tab tabbable with exactly one selected", () => {
    const { rerender } = render(
      <BottomBar
        tabs={TABS}
        activeId="sessions"
        onSelect={() => {}}
        panelId={PANEL_ID}
      />,
    );
    expect(tab(/sessions/i)).toHaveAttribute("tabindex", "0");
    expectExactlyOneSelected("sessions");

    rerender(
      <BottomBar
        tabs={TABS}
        activeId="settings"
        onSelect={() => {}}
        panelId={PANEL_ID}
      />,
    );
    // Exactly the new active tab is tabbable; the prior + other inactive tabs
    // are removed from the tab order.
    expect(tab(/settings/i)).toHaveAttribute("tabindex", "0");
    expect(tab(/sessions/i)).toHaveAttribute("tabindex", "-1");
    expect(tab(/new/i)).toHaveAttribute("tabindex", "-1");
    // Exactly one tab is selected (settings); the prior active (sessions)
    // and the other inactive tab are unselected.
    expectExactlyOneSelected("settings");
  });
});

describe("BottomBar — arrow-key roving navigation", () => {
  it("ArrowRight selects and focuses the next tab, wrapping at the end", () => {
    const onSelect = vi.fn();
    render(<ControlledBottomBar onSelectSpy={onSelect} />);
    const sessions = tab(/sessions/i);
    sessions.focus();
    expect(sessions).toHaveFocus();

    // sessions -> new
    fireEvent.keyDown(sessions, { key: "ArrowRight" });
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenNthCalledWith(1, "new");
    const newTab = tab(/new/i);
    expect(newTab).toHaveFocus();
    expect(newTab).toHaveAttribute("aria-selected", "true");
    expect(newTab).toHaveAttribute("tabindex", "0");

    // new -> settings
    fireEvent.keyDown(newTab, { key: "ArrowRight" });
    expect(onSelect).toHaveBeenCalledTimes(2);
    expect(onSelect).toHaveBeenNthCalledWith(2, "settings");
    const settings = tab(/settings/i);
    expect(settings).toHaveFocus();
    expect(settings).toHaveAttribute("aria-selected", "true");
    expect(settings).toHaveAttribute("tabindex", "0");

    // Wrap: settings -> sessions
    fireEvent.keyDown(settings, { key: "ArrowRight" });
    expect(onSelect).toHaveBeenCalledTimes(3);
    expect(onSelect).toHaveBeenNthCalledWith(3, "sessions");
    const sessionsAgain = tab(/sessions/i);
    expect(sessionsAgain).toHaveFocus();
    expect(sessionsAgain).toHaveAttribute("aria-selected", "true");
    expect(sessionsAgain).toHaveAttribute("tabindex", "0");
  });

  it("ArrowLeft selects and focuses the previous tab, wrapping at the start", () => {
    const onSelect = vi.fn();
    render(<ControlledBottomBar onSelectSpy={onSelect} />);
    const sessions = tab(/sessions/i);
    sessions.focus();
    expect(sessions).toHaveFocus();

    // Wrap: sessions -> settings
    fireEvent.keyDown(sessions, { key: "ArrowLeft" });
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenNthCalledWith(1, "settings");
    const settings = tab(/settings/i);
    expect(settings).toHaveFocus();
    expect(settings).toHaveAttribute("aria-selected", "true");
    expect(settings).toHaveAttribute("tabindex", "0");

    // settings -> new
    fireEvent.keyDown(settings, { key: "ArrowLeft" });
    expect(onSelect).toHaveBeenCalledTimes(2);
    expect(onSelect).toHaveBeenNthCalledWith(2, "new");
    const newTab = tab(/new/i);
    expect(newTab).toHaveFocus();
    expect(newTab).toHaveAttribute("aria-selected", "true");
    expect(newTab).toHaveAttribute("tabindex", "0");
  });

  it("ArrowDown selects and focuses the next tab, wrapping from the last to the first", () => {
    const onSelect = vi.fn();
    render(<ControlledBottomBar onSelectSpy={onSelect} />);
    const sessions = tab(/sessions/i);
    sessions.focus();
    expect(sessions).toHaveFocus();

    // sessions -> new
    fireEvent.keyDown(sessions, { key: "ArrowDown" });
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenNthCalledWith(1, "new");
    const newTab = tab(/new/i);
    expect(newTab).toHaveFocus();
    expect(newTab).toHaveAttribute("aria-selected", "true");
    expect(newTab).toHaveAttribute("tabindex", "0");

    // new -> settings
    fireEvent.keyDown(newTab, { key: "ArrowDown" });
    expect(onSelect).toHaveBeenCalledTimes(2);
    expect(onSelect).toHaveBeenNthCalledWith(2, "settings");
    const settings = tab(/settings/i);
    expect(settings).toHaveFocus();
    expect(settings).toHaveAttribute("aria-selected", "true");
    expect(settings).toHaveAttribute("tabindex", "0");

    // Wrap: settings (last) -> sessions (first) — proves ArrowDown wraps last→first.
    fireEvent.keyDown(settings, { key: "ArrowDown" });
    expect(onSelect).toHaveBeenCalledTimes(3);
    expect(onSelect).toHaveBeenNthCalledWith(3, "sessions");
    const sessionsAgain = tab(/sessions/i);
    expect(sessionsAgain).toHaveFocus();
    expect(sessionsAgain).toHaveAttribute("aria-selected", "true");
    expect(sessionsAgain).toHaveAttribute("tabindex", "0");
  });

  it("ArrowUp selects and focuses the previous tab, wrapping from the first to the last", () => {
    const onSelect = vi.fn();
    render(<ControlledBottomBar onSelectSpy={onSelect} />);
    const sessions = tab(/sessions/i);
    sessions.focus();
    expect(sessions).toHaveFocus();

    // Wrap: sessions (first) -> settings (last).
    fireEvent.keyDown(sessions, { key: "ArrowUp" });
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenNthCalledWith(1, "settings");
    const settings = tab(/settings/i);
    expect(settings).toHaveFocus();
    expect(settings).toHaveAttribute("aria-selected", "true");
    expect(settings).toHaveAttribute("tabindex", "0");

    // settings -> new
    fireEvent.keyDown(settings, { key: "ArrowUp" });
    expect(onSelect).toHaveBeenCalledTimes(2);
    expect(onSelect).toHaveBeenNthCalledWith(2, "new");
    const newTab = tab(/new/i);
    expect(newTab).toHaveFocus();
    expect(newTab).toHaveAttribute("aria-selected", "true");
    expect(newTab).toHaveAttribute("tabindex", "0");
  });
});

describe("BottomBar — Home/End", () => {
  it("Home selects and focuses the first tab", () => {
    const onSelect = vi.fn();
    render(<ControlledBottomBar initial="settings" onSelectSpy={onSelect} />);
    const settings = tab(/settings/i);
    settings.focus();
    expect(settings).toHaveFocus();
    fireEvent.keyDown(settings, { key: "Home" });
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenNthCalledWith(1, "sessions");
    const sessions = tab(/sessions/i);
    expect(sessions).toHaveFocus();
    expect(sessions).toHaveAttribute("aria-selected", "true");
    expect(sessions).toHaveAttribute("tabindex", "0");
    // The previously active tab is no longer tabbable or selected.
    expect(tab(/settings/i)).toHaveAttribute("tabindex", "-1");
    expect(tab(/settings/i)).toHaveAttribute("aria-selected", "false");
  });

  it("End selects and focuses the last tab", () => {
    const onSelect = vi.fn();
    render(<ControlledBottomBar initial="sessions" onSelectSpy={onSelect} />);
    const sessions = tab(/sessions/i);
    sessions.focus();
    expect(sessions).toHaveFocus();
    fireEvent.keyDown(sessions, { key: "End" });
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenNthCalledWith(1, "settings");
    const settings = tab(/settings/i);
    expect(settings).toHaveFocus();
    expect(settings).toHaveAttribute("aria-selected", "true");
    expect(settings).toHaveAttribute("tabindex", "0");
    // The previously active tab is no longer tabbable or selected.
    expect(tab(/sessions/i)).toHaveAttribute("tabindex", "-1");
    expect(tab(/sessions/i)).toHaveAttribute("aria-selected", "false");
  });
});

describe("BottomBar — preventDefault only for handled keys", () => {
  it("prevents default for handled arrow/home/end keys", () => {
    render(
      <BottomBar
        tabs={TABS}
        activeId="sessions"
        onSelect={() => {}}
        panelId={PANEL_ID}
      />,
    );
    const sessions = tab(/sessions/i);
    sessions.focus();
    for (const key of [
      "ArrowRight",
      "ArrowLeft",
      "ArrowUp",
      "ArrowDown",
      "Home",
      "End",
    ]) {
      const ev = createEvent.keyDown(sessions, { key });
      fireEvent(sessions, ev);
      expect(ev.defaultPrevented).toBe(true);
    }
  });

  it("does not prevent default or select for unhandled keys", () => {
    const onSelect = vi.fn();
    render(
      <BottomBar
        tabs={TABS}
        activeId="sessions"
        onSelect={onSelect}
        panelId={PANEL_ID}
      />,
    );
    const sessions = tab(/sessions/i);
    sessions.focus();
    const ev = createEvent.keyDown(sessions, { key: "x" });
    fireEvent(sessions, ev);
    expect(ev.defaultPrevented).toBe(false);
    expect(onSelect).not.toHaveBeenCalled();
  });
});

describe("BottomBar — click behavior unchanged", () => {
  it("clicking a tab selects it", () => {
    const onSelect = vi.fn();
    render(
      <BottomBar
        tabs={TABS}
        activeId="sessions"
        onSelect={onSelect}
        panelId={PANEL_ID}
      />,
    );
    fireEvent.click(tab(/settings/i));
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenCalledWith("settings");
  });
});
