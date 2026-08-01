// Geometric contract for the mobile ask dock (see case.json's description).
//
//   A. Every option label chip renders on ONE line box: a chip squeezed by
//      the option row's flex layout wraps its label inside the chip's fixed
//      24px height, painting text above/below the box (the screenshot this
//      case reproduces).
//   B. The chip never overlaps its option's detail text.
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

  for (const opt of m.options) {
    if (opt.chipLineBoxes !== 1) {
      failures.push(`option "${opt.label}": chip label wrapped onto ${opt.chipLineBoxes} line boxes (chip width ${opt.chipWidth}px in a ${opt.rowWidth}px row)`);
    }
    if (opt.chipOverlapsDetail) {
      failures.push(`option "${opt.label}": chip box overlaps its detail text`);
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
      `transcript squeezed to ${m.transcriptHeight}px of a ${m.host.height}px host (floor: 20%) - the ask consumed the conversation`,
    );
  }

  if (!m.sendButtonPresent) failures.push("Send answers button missing from the harness");

  return failures.length === 0 ? { pass: true, reason: "ask dock geometry holds at phone width" } : { pass: false, reason: failures.join("; ") };
}
