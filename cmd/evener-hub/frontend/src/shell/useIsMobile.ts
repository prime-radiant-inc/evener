import { useCallback, useSyncExternalStore } from "react";

export const MOBILE_QUERY = "(max-width: 899px)";
// The tablet band inside the mobile range: StackHost owns the whole <900px
// range, and at-or-above 768px it trades the drawer for a persistent board
// column (docs/web-ui/design-system.md §12). Same single-source style as
// MOBILE_QUERY so the band can never drift from the shell that hosts it.
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

/** The exact layout query shared by the shell and transcript-display state. */
export function isMobileViewport(): boolean {
  return snapshot(MOBILE_QUERY);
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
  return subscribe(MOBILE_QUERY, listener);
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
  return useMediaQuery(MOBILE_QUERY);
}

// Test-only reset keeps a replaced matchMedia stub from retaining a listener.
export function resetMobileViewportForTests(): void {
  for (const source of sources.values()) {
    detachSource(source);
    source.lastNotifiedMatches = false;
    source.listeners.clear();
  }
}
