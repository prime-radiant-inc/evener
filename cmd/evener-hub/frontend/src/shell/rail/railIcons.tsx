// The rail's drawn icons, on the app's 16x16 icon grid (the same grammar
// as widgets/chevron, openbutton's OpenIcon and the Sessions icon below).
// Gear/Search/Sidebar lead the header; TuneIcon is the rail body's
// organize-by control; PlusIcon is its floating New-session action.
//
// They replace the text glyphs the header used to render. global.css subsets
// Inter to Latin, so those code points came from whatever system fallback
// happened to have them, at whatever stroke weight it drew them - sitting
// beside the app's own SVG chevrons and open-box icon. Drawing them makes the
// weight and the size design decisions rather than a fallback font's.
//
// `aria-hidden` is spelled out on each <svg> rather than carried in the shared
// props below: every one of these is an IconButton's icon, whose `label` is the
// button's only accessible name, and the a11y lint reads the attribute off the
// element rather than through a spread.

import symbolStyles from "../../styles/actionSymbol.module.css";

export const ICON = { viewBox: "0 0 16 16", width: 16, height: 16 } as const;
export const STROKE = {
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 1.5,
  strokeLinecap: "round",
  strokeLinejoin: "round",
} as const;

const CONTROL_ICON = { ...ICON, className: symbolStyles.symbol };

export function HostGlyph({ className = symbolStyles.symbol, testId }: { className?: string; testId?: string }) {
  return (
    <svg data-testid={testId} className={className} {...ICON} aria-hidden="true" focusable="false">
      <rect x="2" y="2.75" width="12" height="4.5" rx="1.25" {...STROKE} />
      <rect x="2" y="8.75" width="12" height="4.5" rx="1.25" {...STROKE} />
      <path d="M4.75 5h.01M4.75 11h.01" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" />
    </svg>
  );
}

export function GearIcon() {
  return (
    <svg {...CONTROL_ICON} aria-hidden="true">
      <circle cx="8" cy="8" r="4.2" {...STROKE} strokeWidth="2.1" />
      <path
        d="M8 1.8v1.4M8 12.8v1.4M1.8 8h1.4M12.8 8h1.4M3.6 3.6l1 1M11.4 11.4l1 1M3.6 12.4l1-1M11.4 4.6l1-1"
        {...STROKE}
        strokeWidth="2.25"
      />
    </svg>
  );
}

// The session list's one create verb, drawn for the floating New-session
// action: two crossing strokes on the same 16x16 grid and stroke weight as
// the rest of the rail's chrome.
export function PlusIcon() {
  return (
    <svg {...CONTROL_ICON} aria-hidden="true">
      <path d="M8 3v10" {...STROKE} />
      <path d="M3 8h10" {...STROKE} />
    </svg>
  );
}

export function SearchIcon() {
  return (
    <svg {...CONTROL_ICON} aria-hidden="true">
      <circle cx="7" cy="7" r="4.25" {...STROKE} />
      <path d="M10.2 10.2 14 14" {...STROKE} />
    </svg>
  );
}

export function SidebarIcon() {
  return (
    <svg {...CONTROL_ICON} aria-hidden="true">
      <rect x="2" y="3" width="12" height="10" rx="1.5" {...STROKE} />
      <path d="M6 3v10" {...STROKE} />
    </svg>
  );
}

// The trailing-panel twin of SidebarIcon (design-system.md's action-symbol
// table): the session header's Overview action and the Overview sidebar's own
// Show/Hide toggle draw this one glyph, as the Sessions rail draws SidebarIcon
// for both of its states.
export function OverviewIcon() {
  return (
    <svg {...CONTROL_ICON} aria-hidden="true">
      <rect x="2" y="3" width="12" height="10" rx="1.5" {...STROKE} />
      <path d="M10 3v10" {...STROKE} />
    </svg>
  );
}

export function MoreIcon() {
  return (
    <svg {...CONTROL_ICON} aria-hidden="true" fill="currentColor">
      <circle cx="3.4" cy="8" r="1.25" />
      <circle cx="8" cy="8" r="1.25" />
      <circle cx="12.6" cy="8" r="1.25" />
    </svg>
  );
}

// The organize-by control's mark: three lines with staggered knobs - the
// cross-platform "arrange this view" convention (Finder's view options,
// Material's tune), so an icon-only control still reads. The knobs are
// round-cap zero-length paths at a heavier stroke, the same dot idiom the
// host glyph uses (RailRow's HostGlyph).
export function TuneIcon() {
  return (
    <svg {...ICON} aria-hidden="true">
      <path d="M2.25 4.25h11.5M2.25 8h11.5M2.25 11.75h11.5" {...STROKE} />
      <path d="M10.75 4.25h.01M5.25 8h.01M9.25 11.75h.01" {...STROKE} strokeWidth={2.5} />
    </svg>
  );
}
