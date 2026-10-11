import { useSyncExternalStore } from "react";

const MOBILE_QUERY = "(max-width: 899px)";
type ViewportListener = () => void;
type MatchMediaFactory = typeof window.matchMedia;

let sourceFactory: MatchMediaFactory | null = null;
let source: MediaQueryList | null = null;
let lastNotifiedMatches = false;
const listeners = new Set<ViewportListener>();

function detachSource(): void {
  if (source !== null && typeof source.removeEventListener === "function")
    source.removeEventListener("change", onSourceChange);
  source = null;
  sourceFactory = null;
}

function onSourceChange(event: MediaQueryListEvent): void {
  const next = event.matches;
  if (next === lastNotifiedMatches) return;
  lastNotifiedMatches = next;
  for (const listener of listeners) listener();
}

function ensureSource(): MediaQueryList | null {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    if (source !== null) detachSource();
    lastNotifiedMatches = false;
    return null;
  }
  if (source !== null && sourceFactory === window.matchMedia) return source;
  detachSource();
  sourceFactory = window.matchMedia;
  source = sourceFactory(MOBILE_QUERY);
  lastNotifiedMatches = source.matches;
  if (typeof source.addEventListener === "function") source.addEventListener("change", onSourceChange);
  return source;
}

function snapshot(): boolean {
  return ensureSource()?.matches ?? false;
}

function subscribe(listener: ViewportListener): () => void {
  ensureSource();
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) detachSource();
  };
}

/** The exact layout query shared by the shell and transcript-display state. */
export function isMobileViewport(): boolean {
  return snapshot();
}

/** Subscribe to the one shared MediaQueryList listener. */
export function subscribeMobileViewport(listener: ViewportListener): () => void {
  return subscribe(listener);
}

export function useIsMobile(): boolean {
  return useSyncExternalStore(subscribe, snapshot, () => false);
}

// Test-only reset keeps a replaced matchMedia stub from retaining a listener.
export function resetMobileViewportForTests(): void {
  detachSource();
  lastNotifiedMatches = false;
  listeners.clear();
}
