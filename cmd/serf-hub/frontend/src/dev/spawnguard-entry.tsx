// Browser-verification harness for the real Spawn pane.
//
// This deliberately renders Spawn with the production widgets, CSS modules,
// and client boundary. The scripted client keeps the page deterministic while
// leaving the browser to answer the questions jsdom cannot: which branch of
// the breakpoint won, where the fixed action band landed, and whether any
// rendered box escaped the viewport.
import { createRoot } from "react-dom/client";
import Spawn from "../panes/spawn/Spawn";
import { FakeClient } from "../protocol/testing/fakeClient";
import { ClientProvider } from "../shell/clientContext";
import { Toast } from "../widgets";
import "../styles/tokens.css";
import "../styles/global.css";

const fake = new FakeClient("ready");
fake.on("serf/harnesses/list", () => ({
  data: [{ id: "serf", label: "serf", kind: "serf" }],
}));
fake.on("serf/launch/schema", () => ({ options: [] }));
fake.on("model/list", () => ({
  data: [
    { provider: "anthropic", model: "claude-sonnet-4-5" },
    { provider: "openai", model: "gpt-5" },
  ],
}));
fake.on("serf/projects/recent", () => ({ data: [] }));
fake.on("serf/paths/complete", () => ({ data: [] }));
fake.on("serf/path/validate", () => ({ path: "", valid: true }));

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("spawnguard.html is missing #root");

document.documentElement.style.height = "100%";
document.body.style.height = "100%";
document.body.style.margin = "0";
document.body.style.background = "var(--surface-0)";
rootEl.style.height = "100%";

createRoot(rootEl).render(
  <ClientProvider client={fake}>
    <div id="spawnguard-pane" style={{ height: "100%" }}>
      <Spawn params={{}} paneId="spawnguard" focused />
    </div>
    <Toast />
  </ClientProvider>,
);

// An 8x4 two-colour PNG, inline - the same fixture image scripts/layoutguard/
// cases/edhz-attachment-tile-single-image uses, for the same reason: staging
// has to be hermetic (no file I/O, no network, no clipboard), and the source
// must NOT be square. An <img> with no height still gets one from its
// intrinsic aspect ratio, so a square source would make .imageThumbnail's
// height:100% redundant and unfalsifiable (docs/testing.md's own
// unfalsifiable-fixture trap).
const SAMPLE_PNG_BASE64 =
  "iVBORw0KGgoAAAANSUhEUgAAAAgAAAAECAIAAAA8r+mnAAAAFUlEQVR4nGP4z/AfK2LAKYFDHLcEAGSoP8FHDbrlAAAAAElFTkSuQmCC";

function samplePngFile(index: number): File {
  const binary = atob(SAMPLE_PNG_BASE64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return new File([bytes], `staged-${index}.png`, { type: "image/png" });
}

// Stages `count` images through the pane's REAL file-picker path - Spawn.tsx's
// hidden <input type=file>, its own handleFilePicker, useAttachments.
// ingestFiles, and the canvas re-encode - so what gets measured afterward is
// the production staging pipeline's own output rather than hand-built tile
// markup that can drift from it (kata 289v; docs/testing.md's
// unfalsifiable-fixture trap). Nothing here is stubbed: this runs in a real
// headless Chrome, where Image decode and canvas.toBlob work, so determinism
// comes from the inline bytes alone.
//
// Resolves only once every tile has settled with a decoded thumbnail. The
// deadline is a failure bound, not a settle wait - it throws rather than
// letting the guard measure a half-staged tree and report it as layout.
async function stageSpawnAttachments(count: number): Promise<number> {
  const input = document.querySelector<HTMLInputElement>('#spawnguard-pane input[type="file"]');
  if (!input) throw new Error("spawn pane has no file input to stage through");
  const transfer = new DataTransfer();
  for (let index = 0; index < count; index++) transfer.items.add(samplePngFile(index));
  input.files = transfer.files;
  input.dispatchEvent(new Event("change", { bubbles: true }));

  const deadline = performance.now() + 10_000;
  for (;;) {
    const thumbnails = Array.from(document.querySelectorAll<HTMLImageElement>('[data-testid="attachment-tile"] img'));
    if (thumbnails.length === count && thumbnails.every((img) => img.complete && img.naturalWidth > 0)) return count;
    if (performance.now() > deadline) {
      throw new Error(`only ${thumbnails.length} of ${count} attachment tiles settled within 10s`);
    }
    await new Promise((resolve) => requestAnimationFrame(resolve));
  }
}

interface Visibility {
  display: string;
  visibility: string;
  width: number;
  height: number;
}

function readVisibility(element: HTMLElement | null, label: string): Visibility | { error: string } {
  if (!element) return { error: `no element matches ${label}` };
  const style = getComputedStyle(element);
  const box = element.getBoundingClientRect();
  return {
    display: style.display,
    visibility: style.visibility,
    width: box.width,
    height: box.height,
  };
}

function visibility(selector: string): Visibility | { error: string } {
  return readVisibility(document.querySelector<HTMLElement>(selector), selector);
}

function isVisible(value: Visibility | { error: string }): boolean {
  return (
    !("error" in value) &&
    value.display !== "none" &&
    value.visibility !== "hidden" &&
    value.width > 0 &&
    value.height > 0
  );
}

function scanHorizontalOverflow(): string[] {
  const findings: string[] = [];
  const viewportRight = document.documentElement.clientWidth;
  for (const element of Array.from(document.querySelectorAll<HTMLElement>("*"))) {
    const style = getComputedStyle(element);
    const box = element.getBoundingClientRect();
    if (box.width <= 1 || box.height <= 1) continue;
    if (box.right > viewportRight + 1)
      findings.push(
        `${element.tagName.toLowerCase()}.${element.className || ""} escapes viewport by ${(box.right - viewportRight).toFixed(1)}px`,
      );
    if ((style.overflowX === "auto" || style.overflowX === "scroll") && element.scrollWidth > element.clientWidth + 1) {
      findings.push(
        `${element.tagName.toLowerCase()}.${element.className || ""} scrolls ${element.scrollWidth - element.clientWidth}px horizontally`,
      );
    }
  }
  if (document.documentElement.scrollWidth > document.documentElement.clientWidth + 1) {
    findings.push(
      `document scrolls ${document.documentElement.scrollWidth - document.documentElement.clientWidth}px horizontally`,
    );
  }
  return findings;
}

interface Box {
  left: number;
  right: number;
  top: number;
  bottom: number;
  width: number;
  height: number;
}

function boxOf(element: HTMLElement): Box {
  const rect = element.getBoundingClientRect();
  return {
    left: rect.left,
    right: rect.right,
    top: rect.top,
    bottom: rect.bottom,
    width: rect.width,
    height: rect.height,
  };
}

// The staged-attachment row is the only part of this pane built from
// fixed-size boxes - 80x80 AttachmentTiles in a flex-wrap row - so it is the
// part with a real chance of escaping a 390px viewport, and it exists only
// once something is staged. Arithmetic says it wraps in time (4x80 + 3x8 =
// 344px at 390px, capped at 8 items); this measures it instead (kata 289v).
function measureAttachments() {
  const row = document.querySelector<HTMLElement>('[data-testid="spawn-attachments"]');
  const tiles = Array.from(document.querySelectorAll<HTMLElement>('[data-testid="attachment-tile"]'));
  return {
    row: row ? boxOf(row) : null,
    tiles: tiles.map((tile) => {
      const thumbnail = tile.querySelector("img");
      return {
        ...boxOf(tile),
        // A tile whose decode never landed still occupies its 80x80 box (the
        // pending slot is sized identically on purpose), so geometry alone
        // cannot tell a settled tile from a stuck one - this can.
        decoded: thumbnail?.complete === true && thumbnail.naturalWidth > 0,
      };
    }),
    rowCount: new Set(tiles.map((tile) => Math.round(tile.getBoundingClientRect().top))).size,
  };
}

function measureSpawn() {
  const actionBand = document.querySelector<HTMLElement>('[data-testid="spawn-mobile-actions"]');
  const actionBox = actionBand?.getBoundingClientRect();
  const actionStyle = actionBand ? getComputedStyle(actionBand) : null;
  const mobileConfigElement = document.querySelector<HTMLElement>('[data-testid="spawn-mobile-config"]');
  const desktopConfigElement = mobileConfigElement?.previousElementSibling as HTMLElement | null;
  const rows = Array.from(document.querySelectorAll<HTMLElement>('[data-testid="mobile-spawn-row"]')).map((row) => {
    const control = row.firstElementChild as HTMLElement | null;
    const sizedElement = control?.matches("button") ? control : row;
    const box = sizedElement.getBoundingClientRect();
    const style = getComputedStyle(sizedElement);
    return {
      label: row.dataset.label ?? "",
      minHeight: style.minHeight,
      height: box.height,
    };
  });

  const heading = document.querySelector<HTMLElement>("[data-testid='spawn-mobile-prompt-intro'] h3");
  const subtitle = document.querySelector<HTMLElement>("[data-testid='spawn-mobile-prompt-intro'] p");

  return {
    viewport: { width: window.innerWidth, height: window.innerHeight },
    mobileConfig: readVisibility(mobileConfigElement, "mobile config"),
    desktopConfig: readVisibility(desktopConfigElement, "desktop config"),
    mobileIntro: visibility('[data-testid="spawn-mobile-prompt-intro"]'),
    desktopTitle: visibility('[data-testid="pane-title-desktop"]'),
    mobileTitle: visibility('[data-testid="pane-title-mobile"]'),
    actionBand: {
      position: actionStyle?.position ?? "missing",
      left: actionBox?.left ?? null,
      right: actionBox?.right ?? null,
      bottom: actionBox ? window.innerHeight - actionBox.bottom : null,
      width: actionBox?.width ?? null,
      minHeight: actionStyle?.minHeight ?? "",
      height: actionBox?.height ?? null,
    },
    rows,
    attachments: measureAttachments(),
    accessiblePrompt: {
      headingTag: heading?.tagName.toLowerCase() ?? "missing",
      headingText: heading?.textContent?.trim() ?? "",
      headingVisible: heading ? isVisible(visibility("[data-testid='spawn-mobile-prompt-intro'] h3")) : false,
      subtitleTag: subtitle?.tagName.toLowerCase() ?? "missing",
      subtitleText: subtitle?.textContent?.trim() ?? "",
      subtitleVisible: subtitle ? isVisible(visibility("[data-testid='spawn-mobile-prompt-intro'] p")) : false,
      headingHiddenFromAT: heading?.getAttribute("aria-hidden") === "true",
      subtitleHiddenFromAT: subtitle?.getAttribute("aria-hidden") === "true",
    },
    overflow: scanHorizontalOverflow(),
  };
}

const settled = new Promise<true>((resolve) => {
  requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(() => resolve(true), 0)));
});

declare global {
  interface Window {
    measureSpawn: typeof measureSpawn;
    settledSpawn: Promise<true>;
    stageSpawnAttachments: typeof stageSpawnAttachments;
  }
}

window.measureSpawn = measureSpawn;
window.settledSpawn = settled;
window.stageSpawnAttachments = stageSpawnAttachments;
