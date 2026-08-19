// The image-attach control's glyph, shared by the two surfaces that stage
// attachments through useAttachments: the session composer and the spawn
// pane's prompt card. A paperclip rather than a plus - the control attaches a
// file, and the plus reads as "add another something" at this size.
//
// Drawn in the app's stroke grammar (fill="none", stroke="currentColor",
// strokeWidth 1.2 - the same as widgets/pathfield's glyphs) so it inherits
// the button variant's own color.
export function AttachIcon() {
  return (
    <svg viewBox="0 0 14 14" width="12" height="12" aria-hidden="true">
      <path
        d="M9.2 4 4.6 8.6a1.7 1.7 0 0 0 2.4 2.4l4.6-4.6a3.1 3.1 0 0 0-4.4-4.4L2.6 6.6a4.5 4.5 0 0 0 6.4 6.4"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
