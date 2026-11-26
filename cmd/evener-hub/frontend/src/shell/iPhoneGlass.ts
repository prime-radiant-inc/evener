import type { GlassScene, GlassSurfaceHandle } from "@glass-sdk/liquid-glass";
import { isAppleTouchDevice } from "../platform";
import { iPhoneHost } from "./iPhoneHost";

const reader = "[data-reading-scroll-host]";
const composer = '[data-iphone-composer="focused"] [data-prompt-card]';
const menus = '[role="menu"], [data-iphone-composer] [role="listbox"]';
const controls = [reader, composer, menus].join(", ");

function visible(element: HTMLElement) {
  return !element.closest("[hidden], [inert]") && element.getClientRects().length > 0;
}

// The scene samples only the current conversation; its controls may live in portals.
export function connectIPhoneGlass() {
  const browser = !iPhoneHost();
  if (browser && !isAppleTouchDevice()) return;
  const body = document.body;
  const contrast = matchMedia("(prefers-contrast: no-preference)");
  const reduce = matchMedia("(prefers-reduced-transparency: reduce)");
  const motion = matchMedia("(prefers-reduced-motion: reduce)");
  const browserBlur =
    browser && (CSS.supports("backdrop-filter", "blur(1px)") || CSS.supports("-webkit-backdrop-filter", "blur(1px)"));
  let sdk: typeof import("@glass-sdk/liquid-glass") | undefined;
  let loading = false;
  let closed = false;
  let source: HTMLElement | undefined;
  let scene: GlassScene | undefined;
  let failed = false;
  let stopTelemetry: (() => void) | undefined;
  const handles = new Map<HTMLElement, GlassSurfaceHandle>();

  function clear() {
    stopTelemetry?.();
    stopTelemetry = undefined;
    for (const element of handles.keys()) delete element.dataset.iphoneGlassDrawn;
    handles.clear();
    scene?.destroy();
    scene = undefined;
  }

  function reconcile() {
    if (closed) return;
    const transparent = contrast.matches && !reduce.matches;
    if (browser) {
      const material = browserBlur && transparent ? "regular" : "opaque";
      if (body.dataset.iphoneGlass !== material) body.dataset.iphoneGlass = material;
    }
    const allowed = body.dataset.iphoneGlass === "regular" && transparent && !motion.matches;
    const card = allowed ? [...body.querySelectorAll<HTMLElement>(composer)].find(visible) : undefined;
    const pane = card?.closest("[data-pane-scaffold-root]");
    const root = pane?.querySelector<HTMLElement>(reader) ?? undefined;
    if (root !== source) {
      clear();
      source = root;
      failed = false;
    }
    if (!card || !root || failed) return;
    if (!sdk) {
      if (!loading) {
        loading = true;
        void import("@glass-sdk/liquid-glass").then(
          (module) => {
            sdk = module;
            reconcile();
          },
          () => {
            loading = false;
            failed = true;
          },
        );
      }
      return;
    }
    if (scene?.error) {
      failed = true;
      clear();
      return;
    }
    try {
      const appearance = getComputedStyle(root).colorScheme === "dark" ? "dark" : "light";
      if (!scene) {
        scene = sdk.createGlassScene({ root, material: "regular", appearance });
      } else if (scene.appearance !== appearance) {
        scene.configure({ appearance });
      }
      const panels = new Set([card]);
      for (const menu of body.querySelectorAll<HTMLElement>(menus)) {
        const triggerId = menu.getAttribute("aria-labelledby");
        const owner = triggerId ? document.getElementById(triggerId) : menu;
        if (pane?.contains(owner) && visible(menu)) panels.add(menu);
      }
      for (const [element, handle] of handles) {
        if (panels.has(element)) continue;
        delete element.dataset.iphoneGlassDrawn;
        handle.remove();
        handles.delete(element);
      }
      let added = false;
      for (const element of panels) {
        if (handles.has(element)) continue;
        handles.set(element, scene.addSurface(element, { floating: element !== card }));
        added = true;
      }
      if (added) {
        // Keep this surface's CSS fill until the worker draws with it attached.
        // The public counter is sampled only during the handoff.
        stopTelemetry?.();
        const active = scene;
        const counter = sdk.TelemetryCounter.workerGlass;
        active.telemetry.setEnabled(true);
        stopTelemetry = active.telemetry.subscribe(({ counters }) => {
          if (active !== scene || active.error || !counters[counter]?.count) return;
          for (const element of handles.keys()) element.dataset.iphoneGlassDrawn = "";
          stopTelemetry?.();
          stopTelemetry = undefined;
          active.telemetry.setEnabled(false);
        });
      }
    } catch {
      failed = true;
      clear();
    }
  }

  const observer = new MutationObserver((changes) => {
    if (
      (source && !source.isConnected) ||
      [...handles.keys()].some((el) => !el.isConnected) ||
      changes.some((change) => {
        if (change.type === "attributes") return true;
        // Streaming and virtual row turnover are the SDK's responsibility.
        if (source?.contains(change.target)) return false;
        return [...change.addedNodes, ...change.removedNodes].some(
          (node) => node instanceof Element && (node.matches(controls) || node.querySelector(controls)),
        );
      })
    )
      reconcile();
  });
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
  observer.observe(body, {
    subtree: true,
    childList: true,
    attributes: true,
    attributeFilter: ["data-iphone-glass", "data-iphone-composer", "data-lg-fallback", "hidden", "inert"],
  });
  contrast.addEventListener("change", reconcile);
  reduce.addEventListener("change", reconcile);
  motion.addEventListener("change", reconcile);
  function retryAfterForeground() {
    if (document.visibilityState !== "visible" || !failed) return;
    failed = false;
    reconcile();
  }
  document.addEventListener("visibilitychange", retryAfterForeground);
  reconcile();
  return () => {
    closed = true;
    observer.disconnect();
    contrast.removeEventListener("change", reconcile);
    reduce.removeEventListener("change", reconcile);
    motion.removeEventListener("change", reconcile);
    document.removeEventListener("visibilitychange", retryAfterForeground);
    clear();
    if (browser) delete body.dataset.iphoneGlass;
  };
}
