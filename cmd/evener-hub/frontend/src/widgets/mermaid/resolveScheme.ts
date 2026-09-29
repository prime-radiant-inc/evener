import { useSyncExternalStore } from "react";
import { usePrefsStore } from "../../stores/prefs";

const DARK_QUERY = "(prefers-color-scheme: dark)";

// jsdom has no matchMedia at all, so these guard exactly the way
// stores/prefs.ts's systemPrefersDark and shell/holdhints/usePrefersReducedMotion
// do: an environment without the API degrades to the dark default rather than
// throwing at render. systemDark therefore reports dark when the query cannot
// be read, matching prefs.ts's pre-existing "system always rendered dark"
// fallback.
function systemDark(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return true;
  return window.matchMedia(DARK_QUERY).matches;
}

function subscribe(callback: () => void): () => void {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return () => {};
  const query = window.matchMedia(DARK_QUERY);
  query.addEventListener("change", callback);
  return () => query.removeEventListener("change", callback);
}

/** The theme actually in effect: the pref, with "system" resolved live. */
export function useResolvedScheme(): "light" | "dark" {
  const theme = usePrefsStore((state) => state.theme);
  const systemIsDark = useSyncExternalStore(subscribe, systemDark);
  if (theme === "system") return systemIsDark ? "dark" : "light";
  return theme;
}

/** Maps the app's CSS tokens (read after the theme applies) onto mermaid's
 * themeVariables knobs, so a diagram picks up the app palette instead of
 * mermaid's own default theme. Tokens come from src/styles/tokens.css.
 *
 * A token that reads back empty (getComputedStyle under jsdom resolves no
 * custom properties, and a real document has none until global.css applies) is
 * omitted rather than passed through: mermaid feeds these values to its color
 * parser (khroma), which throws "Unsupported color format" on an empty string
 * and takes the whole render down. Omitting the key lets mermaid's own default
 * stand in for it; in a browser every token resolves and all survive. */
export function mermaidThemeVariables(scheme: "light" | "dark"): Record<string, string> {
  const computed = getComputedStyle(document.documentElement);
  const variables: Record<string, string> = { dark: String(scheme === "dark") };
  const token = (key: string, name: string) => {
    const value = computed.getPropertyValue(name).trim();
    if (value !== "") variables[key] = value;
  };
  token("background", "--surface-canvas");
  token("primaryColor", "--surface-1");
  token("primaryBorderColor", "--edge");
  token("primaryTextColor", "--ink-hi");
  token("lineColor", "--ink-mid");
  token("fontFamily", "--font-sans");
  return variables;
}
