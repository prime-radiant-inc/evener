// Shared close-button glyph for OverlayPanel (Dialog and, via it, Sheet).
// A plain inline SVG rather than a text glyph so it matches the app's icon
// language (see button gallery's DotIcon) instead of relying on a
// typographic character's cross-font rendering.
export function CloseIcon() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
      <path
        d="M3 3 L13 13 M13 3 L3 13"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        fill="none"
      />
    </svg>
  );
}
