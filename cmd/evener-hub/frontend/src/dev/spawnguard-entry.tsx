// Browser-verification harness for the real Spawn pane.
//
// This deliberately renders Spawn with the production widgets, CSS modules,
// and client boundary. The scripted client keeps the page deterministic while
// leaving the browser to answer the questions jsdom cannot: which branch of
// the breakpoint won, where the prompt card's control row and everything in it
// actually landed, and whether any rendered box escaped the viewport.
import { useState } from "react";
import { createRoot } from "react-dom/client";
import Spawn from "../panes/spawn/Spawn";
import { FakeClient } from "../protocol/testing/fakeClient";
import { ClientProvider } from "../shell/clientContext";
import { PathField, Toast } from "../widgets";
import { isElementVisible } from "./guardVisibility";
import "../styles/tokens.css";
import "../styles/global.css";

const fake = new FakeClient("ready");
fake.on("evener/harnesses/list", () => ({
  data: [{ id: "evener", label: "evener", kind: "evener" }],
}));
fake.on("evener/launch/schema", () => ({ options: [] }));
fake.on("model/list", () => ({
  data: [
    { provider: "anthropic", model: "claude-sonnet-4-5" },
    { provider: "openai", model: "gpt-5" },
    // Deliberately over-long qualified id: the guard picks this through the
    // real picker and asserts the trigger ellipsizes it inside the card
    // instead of pushing effort/Start out.
    { provider: "example-provider-with-a-very-long-name", model: "extra-long-qualifier-model-variant-turbo-01" },
  ],
}));
const directoryRoot = "/home/test/projects/team/experiments/session-start-interface";
const directoryTree = new Map<string, string[]>([
  ["/home/test", [directoryRoot]],
  [directoryRoot, Array.from({ length: 35 }, (_, i) => `${directoryRoot}/folder-${i}`)],
]);
for (const child of directoryTree.get(directoryRoot) ?? []) directoryTree.set(child, []);
fake.on("evener/projects/recent", () => ({ data: [directoryRoot] }));
fake.on("evener/paths/complete", ({ prefix }) => ({ data: directoryTree.get(prefix.replace(/\/+$/, "")) ?? [] }));
fake.on("evener/path/validate", ({ path }) => {
  const resolved = path === "~" ? "/home/test" : path;
  return {
    path: resolved,
    valid: directoryTree.has(resolved),
    error: directoryTree.has(resolved) ? undefined : "Directory not found",
  };
});
fake.on("evener/dirs/create", ({ path }) => {
  directoryTree.set(path, []);
  return { path, created: true };
});
fake.on("evener/plugin/preview", () => ({
  plugins: [
    {
      name: "spawnguard-plugin",
      description: "Spawnguard marketplace tools",
      source: "installed",
      marketplace: "spawnguard",
      selected: true,
      skillCount: 1,
      agentCount: 1,
      commandCount: 1,
      hookCount: 0,
      mcpCount: 0,
    },
    {
      name: "spawnguard-directory",
      description: "Directory-sourced helpers",
      source: "directory",
      path: "/tmp/spawnguard-plugin",
      selected: true,
      skillCount: 0,
      agentCount: 0,
      commandCount: 0,
      hookCount: 1,
      mcpCount: 1,
    },
  ],
}));

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
// height:100% redundant and unfalsifiable (docs/developing-evener/testing.md's own
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
// markup that can drift from it (kata 289v; docs/developing-evener/testing.md's
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
  visible: boolean;
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
    // The verdict is computed HERE, once, by the definition shared with
    // overflowguard (guardVisibility.ts), and travels with the reading. The
    // guard script used to re-derive it from the reported display/visibility
    // pair, and a reading is not the same thing as a rule: that copy silently
    // dropped the geometry clauses, so a probed element under a `display: none`
    // ANCESTOR - which keeps its own computed display, and only its BOX goes to
    // zero - read as visible there while reading as hidden here (kata bsq9).
    // The fixture proving this predicate lives in overflowharness-entry.tsx's
    // visibilityProbe(), which exercises the same function every sweep.
    visible: isElementVisible(element),
  };
}

function visibility(selector: string): Visibility | { error: string } {
  return readVisibility(document.querySelector<HTMLElement>(selector), selector);
}

function isVisible(value: Visibility | { error: string }): boolean {
  return !("error" in value) && value.visible;
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

// The prompt card and everything in its control row. Attach, the model
// trigger, effort, and Start belong INSIDE the card at every width, the way
// the session composer has always had them - this pane used to hand the row a
// class that turned it into a `position: fixed` viewport band on a phone, so
// the paperclip sat at the foot of the screen instead of under the prompt.
// Every reading here is a box the guard compares against the card's own.
function measurePromptCard() {
  const card = document.querySelector<HTMLElement>('[data-testid="spawn-prompt-card"]');
  const controls = document.querySelector<HTMLElement>('[data-testid="spawn-controls"]');
  const field = document.querySelector<HTMLElement>('[data-testid="spawn-prompt-card"] textarea');
  const attach = document.querySelector<HTMLElement>('[data-testid="spawn-attach"]');
  const submit = document.querySelector<HTMLElement>('[data-testid="spawn-submit"]');
  const modelTrigger = document.querySelector<HTMLElement>('[data-testid="spawn-model-trigger"]');
  const modelSlot = document.querySelector<HTMLElement>('[data-testid="spawn-model-slot"]');
  const modelValue = document.querySelector<HTMLElement>('[data-testid="spawn-model-value"]');
  const effort = document.querySelector<HTMLElement>('[data-testid="spawn-effort"]');
  return {
    card: card ? boxOf(card) : null,
    controls: controls ? { ...boxOf(controls), position: getComputedStyle(controls).position } : null,
    field: field ? boxOf(field) : null,
    attach: attach ? boxOf(attach) : null,
    submit: submit ? boxOf(submit) : null,
    modelTrigger: modelTrigger ? boxOf(modelTrigger) : null,
    // The value span inside the trigger: a long qualified model id must
    // ellipsize inside the row, never push effort/Start out of the card.
    modelValue: modelValue
      ? { ...boxOf(modelValue), scrollWidth: modelValue.scrollWidth, clientWidth: modelValue.clientWidth }
      : null,
    effort: effort ? boxOf(effort) : null,
    // The model slot renders at every width now - the verdict is still read
    // from the slot, by the same shared predicate every other reading here
    // uses.
    modelSlot: readVisibility(modelSlot, "spawn model slot"),
  };
}

function measureSpawn() {
  const mobileConfigElement = document.querySelector<HTMLElement>('[data-testid="spawn-mobile-config"]');
  // The remaining desktop-only config surface: the plugin disclosure hides
  // itself below 899px (pluginSelection.module.css's .desktopSurface), so it
  // is the explicit counterpart to the mobile list - not a positional guess
  // at whatever happens to precede the mobile block.
  const desktopConfigElement = document.querySelector<HTMLElement>('[data-testid="spawn-plugin-desktop"]');
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

  const heading = document.querySelector<HTMLElement>("[data-testid='spawn-prompt-intro'] h2");
  const subtitle = document.querySelector<HTMLElement>("[data-testid='spawn-prompt-intro'] p");
  const pluginSummary = document.querySelector<HTMLElement>('[data-testid="spawn-plugin-summary"]');
  const pluginRow = document.querySelector<HTMLElement>('[data-label="Plugins"]');
  const pluginSheet = document.querySelector<HTMLElement>('[role="dialog"][aria-labelledby] h2')?.parentElement
    ?.parentElement;
  const start = document.querySelector<HTMLElement>('[data-testid="spawn-submit"]');
  const pluginList = document.querySelector<HTMLElement>('[data-testid="plugin-selection-list"]');
  const pluginMetadata = document.querySelector<HTMLElement>('[data-testid="plugin-row-metadata"]');
  const pluginSwitches = Array.from(
    document.querySelectorAll<HTMLElement>('[data-testid="plugin-selection-panel"] [role="switch"]'),
  );

  return {
    viewport: { width: window.innerWidth, height: window.innerHeight },
    mobileConfig: readVisibility(mobileConfigElement, "mobile config"),
    desktopConfig: readVisibility(desktopConfigElement, "desktop config"),
    promptIntro: visibility('[data-testid="spawn-prompt-intro"]'),
    desktopTitle: visibility('[data-testid="pane-title-desktop"]'),
    mobileTitle: visibility('[data-testid="pane-title-mobile"]'),
    promptCard: measurePromptCard(),
    rows,
    attachments: measureAttachments(),
    accessiblePrompt: {
      headingTag: heading?.tagName.toLowerCase() ?? "missing",
      headingText: heading?.textContent?.trim() ?? "",
      headingVisible: heading ? isVisible(visibility("[data-testid='spawn-prompt-intro'] h2")) : false,
      subtitleTag: subtitle?.tagName.toLowerCase() ?? "missing",
      subtitleText: subtitle?.textContent?.trim() ?? "",
      subtitleVisible: subtitle ? isVisible(visibility("[data-testid='spawn-prompt-intro'] p")) : false,
      headingHiddenFromAT: heading?.getAttribute("aria-hidden") === "true",
      subtitleHiddenFromAT: subtitle?.getAttribute("aria-hidden") === "true",
    },
    plugins: {
      summary: pluginSummary ? boxOf(pluginSummary) : null,
      row: pluginRow ? boxOf(pluginRow) : null,
      sheet: pluginSheet ? boxOf(pluginSheet) : null,
      start: start ? boxOf(start) : null,
      // The list must expand to fit its rows (the panel owns no scroll
      // container), so the guard reads the computed overflow rather than a box.
      listOverflowY: pluginList ? getComputedStyle(pluginList).overflowY : null,
      // First row's subheading block (source, counts, description under the
      // name) - the layout the rows are contracted to render.
      metadata: pluginMetadata ? boxOf(pluginMetadata) : null,
      switches: pluginSwitches.map(boxOf),
    },
    overflow: scanHorizontalOverflow(),
  };
}

async function settleSpawn(): Promise<true> {
  const deadline = performance.now() + 10_000;
  for (;;) {
    await new Promise((resolve) => requestAnimationFrame(resolve));
    if (document.querySelector('[data-testid="spawn-plugin-disclosure"]')) return true;
    if (performance.now() > deadline) throw new Error("Spawn plugin preview did not settle within 10s");
  }
}

// Picks the harness's long-id model through the REAL picker - trigger,
// combobox filter, option click - so the guard measures the production
// path's own overflow behavior rather than hand-set trigger text that can
// drift from it. Resolves once the trigger's value hook names the long id.
// The input is React-controlled, so the value is set through the native
// setter with a bubbling input event; the option rows are li[role=option]
// carrying the qualified label text (modelCatalog/index.tsx).
async function selectLongSpawnModel(): Promise<true> {
  const trigger = document.querySelector<HTMLButtonElement>('[data-testid="spawn-model-trigger"]');
  if (!trigger) throw new Error("Spawn model trigger is not available");
  trigger.click();
  const deadline = performance.now() + 10_000;
  for (;;) {
    const combo = document.querySelector<HTMLInputElement>('input[role="combobox"]');
    if (combo && combo.value !== "extra-long") {
      const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set;
      setter?.call(combo, "extra-long");
      combo.dispatchEvent(new Event("input", { bubbles: true }));
    }
    const options = Array.from(document.querySelectorAll<HTMLElement>('[role="option"]'));
    const long = options.find((option) => (option.textContent ?? "").includes("extra-long-qualifier"));
    if (long && isElementVisible(long)) {
      long.click();
      break;
    }
    if (performance.now() > deadline) throw new Error("Spawn long model option never appeared");
    await new Promise((resolve) => requestAnimationFrame(resolve));
  }
  for (;;) {
    const value = document.querySelector<HTMLElement>('[data-testid="spawn-model-value"]');
    if (value?.textContent?.includes("extra-long-qualifier")) return true;
    if (performance.now() > deadline) throw new Error("Spawn model trigger never named the long model");
    await new Promise((resolve) => requestAnimationFrame(resolve));
  }
}

function openSpawnPlugins(): void {
  const row = document.querySelector<HTMLButtonElement>('[data-label="Plugins"] button');
  if (row && isElementVisible(row)) {
    row.click();
    return;
  }
  const summary = document.querySelector<HTMLElement>('[data-testid="spawn-plugin-summary"]');
  if (!summary) throw new Error("Spawn Plugins summary is not available");
  summary.click();
}

async function directoryElement<T extends HTMLElement>(selector: string): Promise<T> {
  const deadline = performance.now() + 10_000;
  for (;;) {
    const element = document.querySelector<T>(selector);
    if (element && isElementVisible(element) && !(element instanceof HTMLButtonElement && element.disabled))
      return element;
    if (performance.now() > deadline) throw new Error(`Directory picker did not expose ${selector}`);
    await new Promise((resolve) => requestAnimationFrame(resolve));
  }
}

// Exercise the production picker with a long path and enough children to
// scroll. Geometry must keep the confirmation visible even with a keyboard.
async function exerciseDirectoryPicker() {
  const mobile = window.innerWidth <= 899;
  const trigger = await directoryElement<HTMLButtonElement>(
    mobile ? '[data-label="Working directory"] button' : "#spawn-cwd",
  );
  trigger.focus();
  trigger.click();
  const recent = await directoryElement<HTMLButtonElement>(`button[aria-label="Open recent ${directoryRoot}"]`);
  recent.click();
  await directoryElement<HTMLButtonElement>(`button[aria-label="Open ${directoryRoot}/folder-34"]`);
  const dialog = await directoryElement<HTMLElement>('[role="dialog"]');
  const confirm = Array.from(dialog.querySelectorAll<HTMLButtonElement>("button")).find(
    (button) => button.textContent === "Use this folder",
  );
  if (!confirm) throw new Error("Directory picker has no confirmation");
  const failures: string[] = [];
  function measure(label: string) {
    const panel = dialog.getBoundingClientRect();
    const action = confirm?.getBoundingClientRect();
    const visibleBottom =
      window.innerHeight -
      (mobile ? Number.parseFloat(document.documentElement.style.getPropertyValue("--keyboard-inset")) || 0 : 0);
    if (
      !confirm ||
      !isElementVisible(confirm) ||
      !action ||
      action.width <= 0 ||
      action.height <= 0 ||
      action.top < 0 ||
      action.bottom > visibleBottom + 1
    )
      failures.push(`${label}: confirmation outside visible viewport`);
    if (panel.left < -1 || panel.right > window.innerWidth + 1) failures.push(`${label}: dialog overflows viewport`);
    for (const button of dialog.querySelectorAll("button")) {
      const box = button.getBoundingClientRect();
      if (box.width > 0 && (box.left < panel.left - 1 || box.right > panel.right + 1))
        failures.push(`${label}: control overflows dialog`);
    }
  }
  measure("browse");
  if (mobile) {
    document.documentElement.style.setProperty("--keyboard-inset", "300px");
    measure("keyboard");
  }
  const newFolder = Array.from(dialog.querySelectorAll<HTMLButtonElement>("button")).find(
    (button) => button.textContent === "New folder",
  );
  if (!newFolder) throw new Error("Directory picker has no creation action");
  newFolder.click();
  const nameInput = await directoryElement<HTMLInputElement>('input[autocomplete="off"]:not([aria-label="Path"])');
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  if (!setter) throw new Error("Input value setter missing");
  setter.call(nameInput, "a-new-directory-with-a-long-readable-name");
  nameInput.dispatchEvent(new Event("input", { bubbles: true }));
  await new Promise((resolve) => requestAnimationFrame(resolve));
  nameInput.form?.requestSubmit();
  const created = `${directoryRoot}/a-new-directory-with-a-long-readable-name`;
  const deadline = performance.now() + 10_000;
  // The path can render before the effect restores focus to a persistent control.
  while (
    document.querySelector<HTMLInputElement>('input[aria-label="Path"]')?.value !== created ||
    confirm.disabled ||
    !dialog.contains(document.activeElement)
  ) {
    if (performance.now() > deadline) throw new Error("Directory creation did not settle");
    await new Promise((resolve) => requestAnimationFrame(resolve));
  }
  measure("created");
  confirm.click();
  await new Promise((resolve) => requestAnimationFrame(resolve));
  if (!trigger.textContent?.includes(created)) failures.push("Confirmed path was not stored on the launch form");
  if (document.activeElement !== trigger) failures.push("Confirmation did not restore trigger focus");
  document.documentElement.style.removeProperty("--keyboard-inset");
  return failures;
}

const settled = settleSpawn();

declare global {
  interface Window {
    measureSpawn: typeof measureSpawn;
    settledSpawn: Promise<true>;
    stageSpawnAttachments: typeof stageSpawnAttachments;
    selectLongSpawnModel: typeof selectLongSpawnModel;
    openSpawnPlugins: typeof openSpawnPlugins;
    exerciseDirectoryPicker: typeof exerciseDirectoryPicker;
    exerciseDirectoryField: typeof exerciseDirectoryField;
  }
}

window.measureSpawn = measureSpawn;
window.settledSpawn = settled;
window.stageSpawnAttachments = stageSpawnAttachments;
window.selectLongSpawnModel = selectLongSpawnModel;
window.openSpawnPlugins = openSpawnPlugins;

window.exerciseDirectoryPicker = exerciseDirectoryPicker;

async function exerciseDirectoryField() {
  const path = `${directoryRoot}-a-very-long-final-directory-component`;
  directoryTree.set(path, []);
  function Field() {
    const [value, setValue] = useState(path);
    return (
      <form style={{ margin: 16, width: "calc(100% - 32px)", maxWidth: 340 }}>
        <PathField
          id="shared-directory-field"
          value={value}
          onChange={setValue}
          complete={async (prefix, includeFiles) =>
            (await fake.request("evener/paths/complete", { prefix, includeFiles })).data
          }
          directory={{
            validatePath: (path, kind) => fake.request("evener/path/validate", { path, kind }),
            createDirectory: async (path) => {
              await fake.request("evener/dirs/create", { path });
            },
          }}
        />
      </form>
    );
  }
  const host = document.createElement("div");
  host.style.cssText = "position:fixed;inset:0;z-index:1;background:var(--surface-0)";
  document.body.append(host);
  const root = createRoot(host);
  root.render(<Field />);
  const failures: string[] = [];
  try {
    const trigger = await directoryElement<HTMLButtonElement>("#shared-directory-field");
    const text = trigger.querySelector("span");
    if (
      !text ||
      text.scrollWidth > text.clientWidth + 1 ||
      text.getBoundingClientRect().bottom > trigger.getBoundingClientRect().bottom
    )
      failures.push("Shared directory field truncates or clips its path");
    trigger.focus();
    trigger.click();
    const input = await directoryElement<HTMLInputElement>('input[aria-label="Path"]');
    await new Promise((resolve) => requestAnimationFrame(resolve));
    input.focus();
    if (input.selectionStart !== 0 || input.selectionEnd !== input.value.length)
      failures.push("First path focus did not select the directory");
    const dialog = await directoryElement<HTMLElement>('[role="dialog"]');
    const cancel = Array.from(dialog.querySelectorAll<HTMLButtonElement>("button")).find(
      (button) => button.textContent === "Cancel",
    );
    cancel?.click();
    await new Promise((resolve) => requestAnimationFrame(resolve));
    if (document.activeElement !== trigger) failures.push("Directory field did not restore focus");
  } finally {
    root.unmount();
    host.remove();
  }
  return failures;
}

window.exerciseDirectoryField = exerciseDirectoryField;
