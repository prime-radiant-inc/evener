import { useCallback, useSyncExternalStore } from "react";

export const MOBILE_QUERY = "(max-width: 899px)";
// StackHost already owns the mobile ceiling, so this query only adds the floor.
export const TABLET_QUERY = "(min-width: 768px)";
/** The primary pointer's capability query: fine precision and hover. */
export const FINE_HOVER_POINTER_QUERY = "(hover: hover) and (pointer: fine)";
type ViewportListener = () => void;
type MatchMediaFactory = typeof window.matchMedia;

interface MediaQuerySource {
  factory: MatchMediaFactory | null;
  list: MediaQueryList | null;
  lastNotifiedMatches: boolean;
  readonly listeners: Set<ViewportListener>;
  onChange(event: MediaQueryListEvent): void;
}

const sources = new Map<string, MediaQuerySource>();

function detachSource(source: MediaQuerySource): void {
  if (source.list !== null && typeof source.list.removeEventListener === "function")
    source.list.removeEventListener("change", source.onChange);
  source.list = null;
  source.factory = null;
}

function ensureSource(query: string): MediaQueryList | null {
  let source = sources.get(query);
  if (!source) {
    const created: MediaQuerySource = {
      factory: null,
      list: null,
      lastNotifiedMatches: false,
      listeners: new Set(),
      onChange(event) {
        if (event.matches === created.lastNotifiedMatches) return;
        created.lastNotifiedMatches = event.matches;
        for (const listener of created.listeners) listener();
      },
    };
    source = created;
    sources.set(query, source);
  }
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    if (source.list !== null) detachSource(source);
    source.lastNotifiedMatches = false;
    return null;
  }
  if (source.list !== null && source.factory === window.matchMedia) return source.list;
  detachSource(source);
  source.factory = window.matchMedia;
  source.list = window.matchMedia(query);
  source.lastNotifiedMatches = source.list.matches;
  if (typeof source.list.addEventListener === "function") source.list.addEventListener("change", source.onChange);
  return source.list;
}

function snapshot(query: string): boolean {
  return ensureSource(query)?.matches ?? false;
}

function subscribe(query: string, listener: ViewportListener): () => void {
  ensureSource(query);
  const current = sources.get(query);
  current?.listeners.add(listener);
  return () => {
    current?.listeners.delete(listener);
    if (current?.listeners.size === 0) detachSource(current);
  };
}

/** WebKit exposes the Home Screen window separately from a Safari tab and an embedded WKWebView. */
export function isHomeScreenWindow(): boolean {
  return typeof navigator !== "undefined" && (navigator as Navigator & { standalone?: boolean }).standalone === true;
}

const homeSizeListeners = new Set<ViewportListener>();
let homeObserver: ResizeObserver | null = null;
let homeInsets: { left: number; right: number } | null = null;

function homeScreenInsets() {
  if (homeInsets) return homeInsets;
  const style = getComputedStyle(document.documentElement);
  const insets = {
    left: Number.parseFloat(style.getPropertyValue("--home-safe-left")) || 0,
    right: Number.parseFloat(style.getPropertyValue("--home-safe-right")) || 0,
  };
  if (homeSizeListeners.size) homeInsets = insets;
  return insets;
}

function workspaceViewportWidth(): number {
  if (!isHomeScreenWindow()) return window.innerWidth;
  const { left, right } = homeScreenInsets();
  return window.innerWidth - left - right;
}

function homeScreenEdge() {
  if (!isHomeScreenWindow()) return null;
  const { left, right } = homeScreenInsets();
  // One full touch target must fit on one side, outside the reading area.
  if (left >= 44 && right === 0) return "left";
  if (right >= 44 && left === 0) return "right";
  return null;
}

function onHomeScreenResize() {
  homeInsets = null;
  for (const listener of homeSizeListeners) listener();
}

function subscribeHomeScreenSize(listener: ViewportListener): () => void {
  if (!isHomeScreenWindow()) return () => {};
  if (!homeSizeListeners.size) {
    window.addEventListener("resize", onHomeScreenResize);
    if (typeof ResizeObserver !== "undefined") {
      homeObserver = new ResizeObserver(onHomeScreenResize);
      homeObserver.observe(document.documentElement);
    }
  }
  homeSizeListeners.add(listener);
  return () => {
    homeSizeListeners.delete(listener);
    if (!homeSizeListeners.size) {
      window.removeEventListener("resize", onHomeScreenResize);
      homeObserver?.disconnect();
      homeObserver = null;
      homeInsets = null;
    }
  };
}

function subscribeWorkspaceWidth(listener: ViewportListener): () => void {
  if (isHomeScreenWindow()) return subscribeHomeScreenSize(listener);
  window.addEventListener("resize", listener);
  return () => window.removeEventListener("resize", listener);
}

export function useWorkspaceViewportWidth(): number {
  return useSyncExternalStore(subscribeWorkspaceWidth, workspaceViewportWidth, () => 0);
}

export function useHomeScreenEdge(): "left" | "right" | null {
  return useSyncExternalStore(subscribeHomeScreenSize, homeScreenEdge, () => null);
}

/** The exact layout query shared by the shell and transcript-display state. */
export function isMobileViewport(): boolean {
  return snapshot(MOBILE_QUERY) || (isHomeScreenWindow() && workspaceViewportWidth() < 900);
}

/** Whether the primary pointer hovers with fine precision, the input a pane
 * mount may safely move keyboard focus for. A missing matchMedia (jsdom, SSR)
 * counts as capable, keeping the desktop default those environments render. */
export function hasFineHoverPointer(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return true;
  return window.matchMedia(FINE_HOVER_POINTER_QUERY).matches;
}

/** Subscribe to the one shared MediaQueryList listener. */
export function subscribeMobileViewport(listener: ViewportListener): () => void {
  const stopQuery = subscribe(MOBILE_QUERY, listener);
  const stopResize = subscribeHomeScreenSize(listener);
  return () => {
    stopQuery();
    stopResize();
  };
}

export function useMediaQuery(query: string): boolean {
  const onSubscribe = useCallback((listener: ViewportListener) => subscribe(query, listener), [query]);
  return useSyncExternalStore(
    onSubscribe,
    () => snapshot(query),
    () => false,
  );
}

export function useIsMobile(): boolean {
  return useSyncExternalStore(subscribeMobileViewport, isMobileViewport, () => false);
}

export type WebLayout = "phone" | "tablet" | "desktop";

export function useWebLayout(): WebLayout {
  const mobile = useIsMobile();
  const tablet = useMediaQuery(TABLET_QUERY);
  return mobile ? (tablet ? "tablet" : "phone") : "desktop";
}

// Test-only reset keeps a replaced matchMedia stub from retaining a listener.
export function resetMobileViewportForTests(): void {
  for (const source of sources.values()) {
    detachSource(source);
    source.lastNotifiedMatches = false;
    source.listeners.clear();
  }
}
