// The paper airplane that marks the app's one "send it off" action, shared by
// the two surfaces that do it: the session composer's Send and the spawn
// pane's Start. One glyph for one act - sending a message and starting a
// session are the same gesture on two different cards, so they draw the same
// mark rather than two lookalikes.
//
// Drawn in the app's stroke grammar (fill="none", stroke="currentColor",
// strokeWidth 1.2 - the same as the composer's AttachIcon and
// widgets/pathfield's glyphs) so it inherits the button variant's own color.
export function SendIcon() {
  return (
    <svg viewBox="0 0 14 14" width="12" height="12" aria-hidden="true">
      <path
        d="M12.8 1.2 8.8 12.8 6.4 7.6 1.2 5.2Z M12.8 1.2 6.4 7.6"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
