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
  }
}

window.measureSpawn = measureSpawn;
window.settledSpawn = settled;
