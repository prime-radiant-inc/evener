/**
 * Foundation 7C lane C — responsive Sessions server header.
 *
 * Behavioral tests render long names/origins and all status kinds, and verify
 * the header is one full-width disclosure control (no redundant title row),
 * with an accessible name that includes the server name, full origin, and
 * status, and a chevron hidden from AT. Loading/error/empty behavior and
 * reachability mapping are maintained.
 *
 * CSS contract tests use a structural parser that strips comments, parses
 * exact selector blocks into declaration maps, and asserts exact declarations
 * for each relevant selector. No loose global substring, OR fallback, or
 * comment-match false positives. The parser pins header width/grid/minmax/
 * min-height/font/no-card; text min-width 0; name+origin exact
 * overflow-wrap:anywhere; safe-area composition; and verifies every relevant
 * content selector lacks nowrap/ellipsis/overflow-hidden/fixed-width traps.
 * The responsive invariant is width-independent — it does not fake or claim
 * to measure viewport geometry in JSDOM; the parent real-browser matrix is
 * the geometry gate.
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
const FULL_ORIGIN_FOR_NAME_PROOF = "https://hub.example.com:8443";

// ---------------------------------------------------------------------------
// Structural CSS parser — strips comments, parses selector blocks and
// declaration maps. No loose global substring or comment-match false positives.
// ---------------------------------------------------------------------------

interface CssBlock {
  selector: string;
  declarations: Map<string, string>;
}

function parseCss(source: string): CssBlock[] {
  // Strip /* ... */ comments.
  const stripped = source.replace(/\/\*[\s\S]*?\*\//g, "");
  const blocks: CssBlock[] = [];
  const blockRegex = /([^{}]+)\{([^}]*)\}/g;
  for (;;) {
    const match = blockRegex.exec(stripped);
    if (match === null) break;
    const selector = (match[1] ?? "").trim();
    const body = match[2] ?? "";
    const declarations = new Map<string, string>();
    for (const decl of body.split(";")) {
      const trimmed = decl.trim();
      if (trimmed === "") continue;
      const colonIdx = trimmed.indexOf(":");
      if (colonIdx < 0) continue;
      const property = trimmed.slice(0, colonIdx).trim();
      const value = trimmed.slice(colonIdx + 1).trim();
      declarations.set(property, value);
    }
    blocks.push({ selector, declarations });
  }
  return blocks;
}

function findBlock(blocks: CssBlock[], selector: string): CssBlock | undefined {
  return blocks.find((b) => b.selector === selector);
}

function getDecl(
  blocks: CssBlock[],
  selector: string,
  property: string,
): string | undefined {
  return findBlock(blocks, selector)?.declarations.get(property);
}

const cssSource = readFileSync(
  path.join(__dirname, "SessionsScreen.css"),
  "utf8",
);
const cssBlocks = parseCss(cssSource);

// Selectors that carry header content text — these must not have truncation
// or fixed-width traps. The visually-hidden __label is excluded because it
// uses the standard clip/overflow/nowrap pattern to hide text.
const CONTENT_SELECTORS = [
  ".evener-sessions-header",
  ".evener-sessions-header:focus-visible",
  ".evener-sessions-header__text",
  ".evener-sessions-header__name",
  ".evener-sessions-header__origin",
  ".evener-sessions-header__chevron",
];

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

function renderSessions(
  opts: {
    profiles?: readonly ProfileRedacted[];
    activeProfileId?: string | null;
    reachability?: Record<string, string>;
    failHealth?: boolean;
  } = {},
) {
  // Pass activeProfileId through: explicit null must stay null, not default
  // to "p1".
  const activeId =
    opts.activeProfileId === undefined ? "p1" : opts.activeProfileId;
  const services = createShellServices({
    profiles: opts.profiles ?? PROFILES,
    activeProfileId: activeId,
  });
  if (opts.failHealth) {
    (services.profile as FakeProfileService).failOnce("health");
  }
  const connection = createConnectionStore(services.profile);
  const refresh = connection.getState().refresh();
  if (opts.reachability) {
    for (const [id, state] of Object.entries(opts.reachability)) {
      connection.getState().setReachability(id, state as never);
    }
  }
  const onOpenSwitcher = vi.fn();
  const { container } = render(
    <SessionsScreen connection={connection} onOpenSwitcher={onOpenSwitcher} />,
  );
  return { connection, onOpenSwitcher, container, refresh };
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

  it("status mark is inside the header button, not a detached row", () => {
    const { container } = renderSessions();
    const header = container.querySelector(".evener-sessions-header");
    const mark = container.querySelector(".evener-status-mark");
    expect(header).not.toBeNull();
    expect(mark).not.toBeNull();
    expect(header?.contains(mark)).toBe(true);
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

  it("accessible name includes server name, full origin, and status (exact normalized proof)", async () => {
    renderSessions({
      profiles: [
        { id: "p1", name: "laptop", origin: FULL_ORIGIN_FOR_NAME_PROOF },
      ],
      reachability: { p1: "unknown" },
    });
    const btn = await screen.findByRole("button", {
      name: /laptop.*active server/i,
    });
    // Exact accessible name from natural text content (DOM order: name,
    // origin, visually-hidden "active server", StatusMark label; glyph and
    // chevron are aria-hidden). This fails if scheme or port is removed from
    // the origin.
    expect(btn).toHaveAccessibleName(
      "laptop https://hub.example.com:8443 active server Not checked",
    );
  });

  it("chevron is hidden from assistive tech", () => {
    const { container } = renderSessions();
    const chevron = container.querySelector(".evener-sessions-header__chevron");
    expect(chevron).not.toBeNull();
    expect(chevron?.getAttribute("aria-hidden")).toBe("true");
  });

  it("status glyph is hidden from assistive tech", () => {
    const { container } = renderSessions();
    const glyph = container.querySelector(".evener-status-mark__glyph");
    expect(glyph).not.toBeNull();
    expect(glyph?.getAttribute("aria-hidden")).toBe("true");
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

  it("active profile with missing reachability-map shows Not checked (visible + accessible)", async () => {
    // Active profile exists but no reachability entry for it — must map to
    // unknown / "Not checked", not fabricated "Connected" or "Reconnecting".
    renderSessions({ reachability: {} });
    expect(await screen.findByText(/not checked/i)).toBeInTheDocument();
    const btn = await screen.findByRole("button", {
      name: /laptop.*active server/i,
    });
    expect(btn).toHaveAccessibleName(/not checked/i);
  });

  it("no active profile shows No server and Not checked (after refresh)", async () => {
    const { connection, refresh } = renderSessions({ activeProfileId: null });
    // Wait for refresh to complete — the authoritative state after refresh
    // has no active profile, so "No server" and "Not checked" must appear.
    await refresh;
    expect(connection.getState().status).toBe("ready");
    expect(connection.getState().activeProfileId).toBeNull();
    expect(screen.getByText("No server")).toBeInTheDocument();
    expect(screen.getByText(/not checked/i)).toBeInTheDocument();
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
// CSS contract tests — structural parser, no loose substring
// ---------------------------------------------------------------------------

describe("SessionsScreen.css — structural header contract", () => {
  it("parses all expected selectors", () => {
    for (const selector of CONTENT_SELECTORS) {
      expect(findBlock(cssBlocks, selector)).toBeDefined();
    }
  });

  it("header button is full-width", () => {
    expect(getDecl(cssBlocks, ".evener-sessions-header", "width")).toBe(
      "calc(100% + var(--safe-area-left) + var(--safe-area-right))",
    );
  });

  it("header uses grid layout", () => {
    expect(getDecl(cssBlocks, ".evener-sessions-header", "display")).toBe(
      "grid",
    );
  });

  it("header grid uses minmax(0, 1fr) to prevent overflow", () => {
    expect(
      getDecl(cssBlocks, ".evener-sessions-header", "grid-template-columns"),
    ).toBe("minmax(0, 1fr) auto");
  });

  it("header min 44px tap target", () => {
    expect(getDecl(cssBlocks, ".evener-sessions-header", "min-height")).toBe(
      "var(--tap-target)",
    );
  });

  it("header inherits font (no fixed font-size that breaks AX scale)", () => {
    expect(getDecl(cssBlocks, ".evener-sessions-header", "font")).toBe(
      "inherit",
    );
    const header = findBlock(cssBlocks, ".evener-sessions-header");
    expect(header?.declarations.has("font-size")).toBe(false);
  });

  it("header has no box-shadow (no desktop card)", () => {
    const header = findBlock(cssBlocks, ".evener-sessions-header");
    expect(header?.declarations.has("box-shadow")).toBe(false);
  });

  it("header has no border-radius (no desktop card)", () => {
    const header = findBlock(cssBlocks, ".evener-sessions-header");
    expect(header?.declarations.has("border-radius")).toBe(false);
  });

  it("header expands across both ancestor safe-area insets without a pixel width", () => {
    const width = getDecl(cssBlocks, ".evener-sessions-header", "width");
    expect(width).toBe(
      "calc(100% + var(--safe-area-left) + var(--safe-area-right))",
    );
    expect(width).not.toMatch(/\d+px/);
  });

  it("text column min-width is 0 (prevents grid blowout)", () => {
    expect(
      getDecl(cssBlocks, ".evener-sessions-header__text", "min-width"),
    ).toBe("0");
  });

  it("name uses overflow-wrap: anywhere", () => {
    expect(
      getDecl(cssBlocks, ".evener-sessions-header__name", "overflow-wrap"),
    ).toBe("anywhere");
  });

  it("origin uses overflow-wrap: anywhere", () => {
    expect(
      getDecl(cssBlocks, ".evener-sessions-header__origin", "overflow-wrap"),
    ).toBe("anywhere");
  });

  it("no relevant content selector has white-space: nowrap", () => {
    for (const selector of CONTENT_SELECTORS) {
      const block = findBlock(cssBlocks, selector);
      expect(block?.declarations.get("white-space")).not.toBe("nowrap");
    }
  });

  it("no relevant content selector has text-overflow: ellipsis", () => {
    for (const selector of CONTENT_SELECTORS) {
      const block = findBlock(cssBlocks, selector);
      expect(block?.declarations.get("text-overflow")).not.toBe("ellipsis");
    }
  });

  it("no relevant content selector has overflow: hidden", () => {
    for (const selector of CONTENT_SELECTORS) {
      const block = findBlock(cssBlocks, selector);
      expect(block?.declarations.get("overflow")).not.toBe("hidden");
    }
  });

  it("no relevant content selector has a fixed pixel width", () => {
    for (const selector of CONTENT_SELECTORS) {
      const block = findBlock(cssBlocks, selector);
      const width = block?.declarations.get("width");
      if (width !== undefined) {
        expect(width).not.toMatch(/\d+px/);
      }
    }
  });

  it("no empty CSS rule blocks (every selector has at least one declaration)", () => {
    for (const block of cssBlocks) {
      expect(block.declarations.size).toBeGreaterThan(0);
    }
  });

  it("responsive wrapping is width-independent (overflow-wrap:anywhere on name and origin, no fixed widths)", () => {
    // This invariant does not depend on viewport size. It guarantees that
    // the header wraps long content at any width. The parent real-browser
    // matrix (375/393/430 CSS px, default and AX type) is the geometry
    // gate; JSDOM cannot measure real layout.
    const nameWrap = getDecl(
      cssBlocks,
      ".evener-sessions-header__name",
      "overflow-wrap",
    );
    const originWrap = getDecl(
      cssBlocks,
      ".evener-sessions-header__origin",
      "overflow-wrap",
    );
    expect(nameWrap).toBe("anywhere");
    expect(originWrap).toBe("anywhere");
  });

  // -------------------------------------------------------------------------
  // Safe-area composition — header owns top + horizontal safe edges
  // -------------------------------------------------------------------------

  it("header padding-top includes safe-area-top", () => {
    expect(getDecl(cssBlocks, ".evener-sessions-header", "padding-top")).toBe(
      "calc(var(--space-12) + var(--safe-area-top))",
    );
  });

  it("header padding-right includes safe-area-right", () => {
    expect(getDecl(cssBlocks, ".evener-sessions-header", "padding-right")).toBe(
      "calc(var(--space-16) + var(--safe-area-right))",
    );
  });

  it("header padding-left includes safe-area-left", () => {
    expect(getDecl(cssBlocks, ".evener-sessions-header", "padding-left")).toBe(
      "calc(var(--space-16) + var(--safe-area-left))",
    );
  });

  it("header padding-bottom uses ordinary space token (no safe-area)", () => {
    expect(
      getDecl(cssBlocks, ".evener-sessions-header", "padding-bottom"),
    ).toBe("var(--space-12)");
  });

  it("header negative margin-left compensates shell safe-area-left", () => {
    expect(getDecl(cssBlocks, ".evener-sessions-header", "margin-left")).toBe(
      "calc(-1 * var(--safe-area-left))",
    );
  });

  it("header negative margin-right compensates shell safe-area-right", () => {
    expect(getDecl(cssBlocks, ".evener-sessions-header", "margin-right")).toBe(
      "calc(-1 * var(--safe-area-right))",
    );
  });
});
