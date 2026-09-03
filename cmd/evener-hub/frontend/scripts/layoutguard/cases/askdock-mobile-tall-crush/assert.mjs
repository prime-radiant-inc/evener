// Geometric contract for the mobile ask dock as the transcript's trailing
// row (see case.json's description):
//
//   A. An option's label never overlaps its own detail text, compared
//      LINE BOX by line box: label and detail are inline siblings in one
//      prose flow (ask-dialog UX rework), so their element bounding boxes
//      are unions over wrapped lines and intersect by construction - only
//      same-line glyph boxes are a meaningful overlap signal.
//   B. No option row escapes the card horizontally: option labels are
//      inline bold text (the fixed-height chip this case was originally
//      authored against is gone), so a long or UNBROKEN label WRAPS onto
//      as many lines as it needs instead of painting outside its box or
//      pushing the row past the card's edge.
//   C. The dock sizes to its content: no internal scroll boundary
//      (scrollHeight ~= clientHeight, overflow-y visible). Clipping here
//      would strand the note row and Send answers behind a scrollbar the
//      reader can't reach by scrolling the transcript - and in the real
//      mount it would lie to the virtual list's measureElement.
//   D. The transcript owns the scrolling: a tall batch makes its
//      scrollHeight exceed its client height with overflow-y auto, so the
//      whole batch stays reachable by ordinary transcript scrolling.
//   E. The pending ask changes no allocation: the transcript's client
//      height and the footer's box are identical with the dock present and
//      hidden. (The old design's "transcript keeps 20% of the pane" floor
//      is obsolete: the dock no longer competes for the pane's flex space
//      at all.)
//   F. The transcript box stays inside the host pane and the footer below
//      it, and the outer document never scrolls.
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

  const { withDock, withoutDock } = m;

  if (withDock.dock.scrollHeight > withDock.dock.clientHeight + 1) {
    failures.push(
      `dock clips its own content (${withDock.dock.scrollHeight}px content in ${withDock.dock.clientHeight}px box) - the dock must size to content and let the transcript scroll`,
    );
  }
  if (withDock.dock.overflowY !== "visible") {
    failures.push(`dock carries its own overflow-y: ${withDock.dock.overflowY} - scrolling belongs to the transcript`);
  }

  if (withDock.transcriptScrollHeight <= withDock.transcriptClientHeight) {
    failures.push(
      `a tall batch did not extend the transcript's scroll region (${withDock.transcriptScrollHeight}px content in ${withDock.transcriptClientHeight}px)`,
    );
  }
  if (withDock.transcriptOverflowY !== "auto" && withDock.transcriptOverflowY !== "scroll") {
    failures.push(`transcript overflow belongs to ${withDock.transcriptOverflowY}, not the transcript`);
  }

  if (withDock.transcriptClientHeight !== withoutDock.transcriptClientHeight) {
    failures.push(
      `the pending ask changed the transcript's allocation (${withoutDock.transcriptClientHeight}px without it, ${withDock.transcriptClientHeight}px with)`,
    );
  }
  if (Math.abs(withDock.footerRect.top - withoutDock.footerRect.top) > 1) {
    failures.push(`the pending ask moved the footer (${withoutDock.footerRect.top}px -> ${withDock.footerRect.top}px)`);
  }

  if (withDock.transcriptRect.bottom > withDock.footerRect.top + 1) {
    failures.push(
      `transcript overlaps the footer by ${(withDock.transcriptRect.bottom - withDock.footerRect.top).toFixed(1)}px`,
    );
  }
  if (withDock.footerRect.bottom > m.host.bottom + 1) {
    failures.push(`footer escapes the host pane by ${(withDock.footerRect.bottom - m.host.bottom).toFixed(1)}px`);
  }
  if (m.documentHeight > m.viewport.h + 1) {
    failures.push(`outer document scrolls: ${m.documentHeight}px document in ${m.viewport.h}px viewport`);
  }

  if (!m.sendButtonPresent) failures.push("Send answers button missing from the harness");

  return failures.length === 0
    ? { pass: true, reason: "ask dock geometry holds at phone width: labels wrap inside the card, the dock sizes to content, and the transcript owns the scroll" }
    : { pass: false, reason: failures.join("; ") };
}
