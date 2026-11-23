// Shared close-button glyph for OverlayPanel (Dialog and, via it, Sheet),
// every other icon-only close/dismiss control, and the 10px remove controls
// that used to each draw their own private cross (Chip, CollectionEditor,
// AttachmentTile).
//
// A plain inline SVG rather than a text glyph so it matches the app's icon
// language (see button gallery's DotIcon) instead of relying on a
// typographic character's cross-font rendering.
//
// One definition at every size. The path stays on the app's 16x16 icon grid;
// the stroke is specified in RENDERED pixels, so the attribute compensates
// for the viewBox's own scaling. Without that compensation the fixed 1.5
// attribute paints 1.5 * size / 16: 0.94px when the 16-grid glyph renders in
// a 10px box, which is why the old 10px remove glyphs kept a separate 12-grid
// path instead of reusing this one. The default 14px box is the close
// buttons' size; the remove buttons pass size={10}.
const GRID = 16;
const STROKE_PX = 1.5;
const DEFAULT_SIZE = 14;

export interface CloseIconProps {
  /** Box edge in px. 14px is the close buttons' size; the 10px remove
   * controls pass 10. The rendered stroke stays 1.5px at either size. */
  size?: number;
}

export function CloseIcon({ size = DEFAULT_SIZE }: CloseIconProps = {}) {
  // Rounded to two decimals: the attribute is an implementation detail, the
  // rendered 1.5px stroke is the contract.
  const strokeWidth = Math.round(((STROKE_PX * GRID) / size) * 100) / 100;
  return (
    <svg viewBox={`0 0 ${GRID} ${GRID}`} width={size} height={size} aria-hidden="true">
      <path
        d="M3 3 L13 13 M13 3 L3 13"
        stroke="currentColor"
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        fill="none"
      />
    </svg>
  );
}
