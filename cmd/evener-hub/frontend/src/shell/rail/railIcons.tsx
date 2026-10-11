// The rail's drawn icons, on the app's 16x16 icon grid (the same grammar
// as widgets/chevron, openbutton's OpenIcon and TreeDrawer's
// SessionsIcon). Gear/Search/Sidebar lead the header; TuneIcon is the rail
// body's organize-by control.
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

const ICON = { viewBox: "0 0 16 16", width: 16, height: 16 } as const;
const STROKE = {
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 1.5,
  strokeLinecap: "round",
  strokeLinejoin: "round",
} as const;

export function GearIcon() {
  return (
    <svg {...ICON} aria-hidden="true">
      <circle cx="8" cy="8" r="2.25" {...STROKE} />
      <path
        d="M8 1.75v2M8 12.25v2M1.75 8h2M12.25 8h2M3.6 3.6l1.4 1.4M11 11l1.4 1.4M3.6 12.4 5 11M11 5l1.4-1.4"
        {...STROKE}
      />
    </svg>
  );
}

export function SearchIcon() {
  return (
    <svg {...ICON} aria-hidden="true">
      <circle cx="7" cy="7" r="4.25" {...STROKE} />
      <path d="M10.2 10.2 14 14" {...STROKE} />
    </svg>
  );
}

export function SidebarIcon() {
  return (
    <svg {...ICON} aria-hidden="true">
      <rect x="2" y="3" width="12" height="10" rx="1.5" {...STROKE} />
      <path d="M6 3v10" {...STROKE} />
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
