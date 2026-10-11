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
