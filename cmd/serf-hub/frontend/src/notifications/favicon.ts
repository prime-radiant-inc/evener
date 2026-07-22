// The favicon channel: a single link[rel="icon"] carrying an inline
// data:image/svg+xml — a neutral base circle plus, when the favicon pref is
// on, one corner dot colored by the highest-priority active attention level.
//
// The dot colors are PINNED dark-theme constants regardless of the page's
// active light/dark theme (floor §3.3, notifications.js:35-44): the favicon
// renders against dark browser chrome, not the app surface. This is the ONE
// sanctioned non-token color site in the wave — the literals live on the
// generated SVG, never in tokens.css, precisely because tokens.css tracks
// the app-surface theme these must not follow.
import type { AttentionSummary } from "../stores/tree";

const DOT_ERROR = "#f7768e";
const DOT_NEEDS_YOU = "#e0af68";
const DOT_WORKING = "#7aa2f7";
// Neutral base circle (dark --ink-3): a blue base would read as "working" at
// rest. Dot stroke matches the dark chrome so the corner dot reads cleanly.
const BASE_CIRCLE = "#7e8593";
const DOT_STROKE = "#1a1b26";

// Highest-priority active level, checked error > needs_you > working; null
// (no dot) when none apply — idle never draws a dot.
export function dotColorFor(summary: AttentionSummary): string | null {
  if (summary.error > 0) return DOT_ERROR;
  if (summary.needsYou > 0) return DOT_NEEDS_YOU;
  if (summary.working > 0) return DOT_WORKING;
  return null;
}

// Inline data URI, rebuilt fresh per apply. Only '#' is percent-encoded
// (%23) — byte-for-byte the legacy's own encoding (notifications.js:126-139)
// so the two favicons are indistinguishable to a parity diff.
export function buildFaviconDataURI(dotColor: string | null): string {
  const dot = dotColor
    ? `<circle cx='78' cy='78' r='18' fill='${dotColor}' stroke='${DOT_STROKE}' stroke-width='4'/>`
    : "";
  const svg =
    `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'>` +
    `<circle cx='50' cy='50' r='40' fill='${BASE_CIRCLE}'/>${dot}</svg>`;
  return `data:image/svg+xml;utf8,${svg.replace(/#/g, "%23")}`;
}

const PLAIN_FAVICON = buildFaviconDataURI(null);

// Targets (or creates, if missing) the single link[rel="icon"] element.
function setFavicon(href: string): void {
  let link = document.querySelector<HTMLLinkElement>("link[rel='icon']");
  if (!link) {
    link = document.createElement("link");
    link.rel = "icon";
    document.head.appendChild(link);
  }
  link.href = href;
}

// favicon pref OFF ⇒ the plain neutral favicon, no state indicator at all;
// ON ⇒ the highest-priority dot (or plain, when nothing is active or no
// summary has loaded). The pref comes from the shipped prefs store — this
// function NEVER defaults it on (floor §3.1 all-OFF; the top cross-wave trap).
export function applyFavicon(faviconOn: boolean, summary: AttentionSummary | null): void {
  if (!faviconOn) {
    setFavicon(PLAIN_FAVICON);
    return;
  }
  const color = summary ? dotColorFor(summary) : null;
  setFavicon(buildFaviconDataURI(color));
}
