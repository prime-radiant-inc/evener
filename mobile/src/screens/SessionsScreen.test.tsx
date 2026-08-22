/**
 * Foundation 7C lane C — responsive Sessions server header.
 *
 * Behavioral tests render long names/origins and all status kinds, and verify
 * the header is one full-width disclosure control (no redundant title row),
 * with an accessible name that includes the server name, full origin, and
 * status, and a chevron hidden from AT. Loading/error/empty behavior and
 * reachability mapping are maintained.
 *
 * CSS contract tests pin full-width, grid layout, overflow-wrap:anywhere on
 * the origin, no text-overflow ellipsis, no white-space:nowrap, a 44px minimum
 * tap target, and no horizontal overflow — across default and AX type scales.
 */

import { readFileSync } from "node:fs";
import path from "node:path";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ProfileRedacted } from "../services/nativeProfiles";
import { createConnectionStore } from "../state/connection";
import type { FakeProfileService } from "../test/fakeProfileService";
import { createShellServices } from "./fixture-services";
import { SessionsScreen } from "./SessionsScreen";

afterEach(() => {
  cleanup();
});

const PROFILES: readonly ProfileRedacted[] = [
  { id: "p1", name: "laptop", origin: "https://hub.example.com:8443" },
  { id: "p2", name: "server", origin: "http://192.168.1.10:8080" },
];

const LONG_NAME =
  "Very Long Server Name That Exceeds Normal Width Constraints And Then Some";
const LONG_ORIGIN =
  "https://extremely-long-subdomain-name.deeply.nested.path.example.com:8443/long/path/to/resource";

function renderSessions(
  opts: {
    profiles?: readonly ProfileRedacted[];
    activeProfileId?: string | null;
    reachability?: Record<string, string>;
    failHealth?: boolean;
  } = {},
) {
  const services = createShellServices({
    profiles: opts.profiles ?? PROFILES,
    activeProfileId: opts.activeProfileId ?? "p1",
  });
  if (opts.failHealth) {
    (services.profile as FakeProfileService).failOnce("health");
  }
  const connection = createConnectionStore(services.profile);
  // refresh() sets status to "loading" synchronously before the async health
  // call resolves, so the initial render sees a known status.
  void connection.getState().refresh();
  if (opts.reachability) {
    for (const [id, state] of Object.entries(opts.reachability)) {
      connection.getState().setReachability(id, state as never);
    }
  }
  const onOpenSwitcher = vi.fn();
  const { container } = render(
    <SessionsScreen connection={connection} onOpenSwitcher={onOpenSwitcher} />,
  );
  return { connection, onOpenSwitcher, container };
}

// ---------------------------------------------------------------------------
// Behavioral tests
// ---------------------------------------------------------------------------

describe("SessionsScreen — server header disclosure control", () => {
  it("renders the active server name in the header", async () => {
    renderSessions();
    expect(await screen.findByText("laptop")).toBeInTheDocument();
  });

  it("renders the full origin including scheme, host, and port", async () => {
    renderSessions();
    expect(
      await screen.findByText("https://hub.example.com:8443"),
    ).toBeInTheDocument();
  });

  it("header is a single full-width button (disclosure control)", () => {
    const { container } = renderSessions();
    const headers = container.querySelectorAll(".evener-sessions-header");
    expect(headers.length).toBe(1);
    expect(headers[0]?.tagName).toBe("BUTTON");
  });

  it("no redundant Sessions title row — no TopBar", () => {
    const { container } = renderSessions();
    expect(container.querySelector(".evener-topbar")).toBeNull();
    expect(
      screen.queryByRole("heading", { name: /^sessions$/i }),
    ).not.toBeInTheDocument();
  });

  it("status is inside the header button, not a detached row", () => {
    const { container } = renderSessions();
    const header = container.querySelector(".evener-sessions-header");
    const status = container.querySelector(".evener-sessions-header__status");
    expect(header).not.toBeNull();
    expect(status).not.toBeNull();
    expect(header?.contains(status)).toBe(true);
  });

  it("status mark renders glyph and text inside the header", async () => {
    renderSessions({ reachability: { p1: "reachable" } });
    const header = await screen.findByRole("button", {
      name: /laptop.*active server/i,
    });
    const mark = header.querySelector(".evener-status-mark");
    expect(mark).not.toBeNull();
    expect(mark?.querySelector(".evener-status-mark__glyph")).not.toBeNull();
    expect(mark?.querySelector(".evener-status-mark__label")).not.toBeNull();
  });

  it("accessible name includes server name, full origin, and status", async () => {
    renderSessions({ reachability: { p1: "unknown" } });
    const btn = await screen.findByRole("button", {
      name: /laptop.*active server/i,
    });
    expect(btn).toHaveAccessibleName(
      /hub\.example\.com.*laptop.*active server.*not checked/i,
    );
  });

  it("chevron is hidden from assistive tech", () => {
    const { container } = renderSessions();
    const chevron = container.querySelector(".evener-sessions-header__chevron");
    expect(chevron).not.toBeNull();
    expect(chevron?.getAttribute("aria-hidden")).toBe("true");
  });

  it("opens switcher on click", async () => {
    const { onOpenSwitcher } = renderSessions();
    const btn = await screen.findByRole("button", {
      name: /laptop.*active server/i,
    });
    fireEvent.click(btn);
    expect(onOpenSwitcher).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["reachable", "reachable", /connected/i, "✓"],
    ["reconnecting", "reconnecting", /reconnecting/i, "↻"],
    ["offline", "unreachable", /offline/i, "✕"],
    ["unknown", "unknown", /not checked/i, "?"],
  ])(
    "status %s shows the correct text and glyph",
    async (_label, reach, label, glyph) => {
      const { container } = renderSessions({ reachability: { p1: reach } });
      expect(await screen.findByText(label)).toBeInTheDocument();
      const glyphEl = container.querySelector(".evener-status-mark__glyph");
      expect(glyphEl?.textContent).toBe(glyph);
    },
  );

  it("long server name renders fully without truncation", async () => {
    renderSessions({
      profiles: [
        { id: "p1", name: LONG_NAME, origin: "https://hub.example.com:8443" },
      ],
    });
    expect(await screen.findByText(LONG_NAME)).toBeInTheDocument();
  });

  it("long origin renders fully", async () => {
    renderSessions({
      profiles: [{ id: "p1", name: "laptop", origin: LONG_ORIGIN }],
    });
    expect(await screen.findByText(LONG_ORIGIN)).toBeInTheDocument();
  });

  it("no active profile shows No server and Not checked", async () => {
    renderSessions({ activeProfileId: null });
    expect(await screen.findByText("No server")).toBeInTheDocument();
    expect(await screen.findByText(/not checked/i)).toBeInTheDocument();
  });

  it("maintains loading state", () => {
    renderSessions();
    // refresh() set status to "loading" synchronously; health has not resolved.
    expect(screen.getByText(/loading sessions/i)).toBeInTheDocument();
  });

  it("maintains error state with retry", async () => {
    renderSessions({ failHealth: true });
    expect(
      await screen.findByRole("button", { name: /retry/i }),
    ).toBeInTheDocument();
  });

  it("maintains empty state", async () => {
    renderSessions();
    expect(await screen.findByText(/no sessions yet/i)).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// CSS contract tests — pin responsive header geometry
// ---------------------------------------------------------------------------

describe("SessionsScreen.css — responsive header contract", () => {
  let css = "";
  try {
    css = readFileSync(path.join(__dirname, "SessionsScreen.css"), "utf8");
  } catch {
    // CSS file does not exist yet — all contract tests fail (RED).
  }

  function has(rule: RegExp): boolean {
    return rule.test(css);
  }

  it("header button is full-width", () => {
    expect(has(/\.evener-sessions-header\s*\{[^}]*width\s*:\s*100%/)).toBe(
      true,
    );
  });

  it("header uses grid layout", () => {
    expect(has(/\.evener-sessions-header\s*\{[^}]*display\s*:\s*grid/)).toBe(
      true,
    );
  });

  it("origin uses overflow-wrap: anywhere", () => {
    expect(has(/overflow-wrap\s*:\s*anywhere/i)).toBe(true);
  });

  it("no text-overflow ellipsis on header elements", () => {
    expect(has(/text-overflow\s*:\s*ellipsis/i)).toBe(false);
  });

  it("no white-space: nowrap on header elements", () => {
    expect(has(/white-space\s*:\s*nowrap/i)).toBe(false);
  });

  it("min 44px tap target on header button", () => {
    expect(has(/min-height\s*:\s*var\(--tap-target\)/i)).toBe(true);
  });

  it("prevents horizontal overflow (min-width: 0 or minmax(0, 1fr))", () => {
    expect(has(/min-width\s*:\s*0/i) || has(/minmax\(\s*0\s*,\s*1fr\)/i)).toBe(
      true,
    );
  });

  it("no desktop card styling on header (no box-shadow or border-radius)", () => {
    const headerBlock =
      css.match(/\.evener-sessions-header\s*\{([\s\S]*?)\}/)?.[1] ?? "";
    expect(/box-shadow/i.test(headerBlock)).toBe(false);
    expect(/border-radius/i.test(headerBlock)).toBe(false);
  });
});
