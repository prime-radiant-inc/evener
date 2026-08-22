// Geometric contract for the mobile ask dock (see case.json's description).
//
//   A. An option's label never overlaps its own detail text, compared
//      LINE BOX by line box: label and detail are inline siblings in one
//      prose flow (ask-dialog UX rework), so their element bounding boxes
//      are unions over wrapped lines and intersect by construction - only
//      same-line glyph boxes are a meaningful overlap signal.
//   B. No option row escapes the card horizontally: option labels are
//      inline bold text (the fixed-height chip this case was originally
//      authored against is gone), so a long valid label WRAPS onto as many
//      lines as it needs instead of painting outside its box or pushing
//      the row past the card's edge.
//   C. The dock's own box stays inside the host pane: the pane clips
//      overflow:hidden, so a dock taller than the pane puts its own bottom
//      (note row, batch footer, Send answers) out of reach entirely.
//   D. A dock whose content exceeds its box is internally scrollable
//      (overflow-y: auto/scroll) - that, not pane growth, is how a tall
//      batch stays answerable.
//   E. The transcript keeps at least 20% of the host height: a pending ask
//      must not erase the conversation it asks about.
export default function assert(m) {
  const failures = [];

  if (m.host.height >= m.viewport.h) {
    failures.push(`fixture host is ${m.host.height}px, not shorter than the ${m.viewport.h}px viewport`);
  }

  for (const opt of m.options) {
    if (opt.labelOverlapsDetail) {
      failures.push(`option "${opt.label}": label box overlaps its detail text`);
    }
    if (opt.escapesCard) {
      failures.push(`option "${opt.label}": row (${opt.rowWidth}px) or its label escapes the card horizontally`);
    }
  }

  if (!m.dock.containedInHost) {
    failures.push(
      `dock escapes the host pane (dock ${m.dock.rect.top}..${m.dock.rect.bottom}, host ${m.host.top}..${m.host.bottom}) - the pane's overflow:hidden clips whatever hangs out, Send answers included`,
    );
  }

  if (m.dock.scrollHeight > m.dock.clientHeight + 1 && m.dock.overflowY !== "auto" && m.dock.overflowY !== "scroll") {
    failures.push(
      `dock content (${m.dock.scrollHeight}px) exceeds its box (${m.dock.clientHeight}px) with overflow-y: ${m.dock.overflowY} - unreachable, not scrollable`,
    );
  }

  const transcriptFloor = 0.2 * m.host.height;
  if (m.transcriptHeight < transcriptFloor) {
    failures.push(
      `transcript squeezed to ${m.transcriptHeight}px of a ${m.host.height}px host (floor: 20%); dock ${m.dock.rect.top}..${m.dock.rect.bottom}, ${m.dock.clientHeight}px client/${m.dock.scrollHeight}px content - the ask consumed the conversation`,
    );
  }

  if (!m.sendButtonPresent) failures.push("Send answers button missing from the harness");

  return failures.length === 0 ? { pass: true, reason: "ask dock geometry holds at phone width" } : { pass: false, reason: failures.join("; ") };
}
