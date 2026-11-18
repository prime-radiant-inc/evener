// Shared assertions for the wide-touch document-lock pair (issue #6471).
//
// `overscroll-touch-wide` requests a coarse pointer at 1280px; its `-fine`
// twin uses the same markup at the same width with no touch screen. One case
// proves the gesture lock reaches a wide touch screen, the other proves it
// stops at the touching surfaces: a desktop window keeps the browser's own
// document gestures, and both keep the desktop layout's document scrolling
// and pane scrolling exactly as they were.
//
// Two-sided and NaN-safe: a missing measurement reads as `undefined`, which
// fails an equality check rather than slipping past an open inequality.
export function expectDocumentLock({ coarse }) {
  return (m) => {
    const failures = [];
    const tolerance = 1;
    const mode = coarse ? "coarse-pointer" : "fine-pointer";

    if (m.viewport.width < 900) {
      failures.push(
        `the viewport is ${m.viewport.width}px wide - under the 900px breakpoint the phone gate alone applies, so this case proves nothing about a wide surface`,
      );
    }
    if (m.anyPointerCoarse !== coarse) {
      failures.push(
        `(any-pointer: coarse) is ${m.anyPointerCoarse} - the ${mode} emulation this case names did not apply`,
      );
    }

    const document = m.document ?? {};
    const expectedOverscroll = coarse ? "none" : "auto";
    for (const axis of ["X", "Y"]) {
      const actual = document[`overscrollBehavior${axis}`];
      if (actual !== expectedOverscroll) {
        failures.push(
          `html overscroll-behavior-${axis.toLowerCase()} is ${actual}, expected ${expectedOverscroll} on a wide ${mode} surface`,
        );
      }
      // The layout lock is the phone's alone: a desktop layout at a wide width
      // keeps a scrollable document whether or not it has a touch screen.
      const overflow = document[`rootOverflow${axis}`];
      if (overflow !== "visible") {
        failures.push(`html overflow-${axis.toLowerCase()} is ${overflow}, expected visible on a wide ${mode} surface`);
      }
      const bodyOverflow = document[`bodyOverflow${axis}`];
      if (bodyOverflow !== "visible") {
        failures.push(
          `body overflow-${axis.toLowerCase()} is ${bodyOverflow}, expected visible on a wide ${mode} surface`,
        );
      }
    }

    const expectedTouchAction = coarse ? "pan-x pan-y" : "auto";
    if (document.bodyTouchAction !== expectedTouchAction) {
      failures.push(
        `body touch-action is ${document.bodyTouchAction}, expected ${expectedTouchAction} - the gesture lock and the touch-zoom lock must agree about this surface`,
      );
    }

    if (document.scrollTopAfterAttempt !== 0) {
      failures.push(
        `the document scrolled to ${document.scrollTopAfterAttempt}px, expected the desktop shell to fit the viewport`,
      );
    }
    if (!(document.scrollHeight <= document.clientHeight + tolerance)) {
      failures.push(
        `the document is ${document.scrollHeight}px tall in a ${document.clientHeight}px viewport - the desktop shell must fit, so document scrolling is genuinely available and unused`,
      );
    }

    const shell = m.shell ?? {};
    if (!(Math.abs(shell.height - m.viewport.height) <= tolerance)) {
      failures.push(
        `the shell is ${shell.height}px tall in a ${m.viewport.height}px viewport, expected the full viewport`,
      );
    }
    if (!(shell.bottom <= m.viewport.height + tolerance)) {
      failures.push(`the shell escapes the viewport by ${(shell.bottom - m.viewport.height).toFixed(1)}px`);
    }

    const paneBody = m.paneBody ?? {};
    if (paneBody.overflowY !== "auto") {
      failures.push(`the pane body's overflow-y is ${paneBody.overflowY}, expected auto - it owns the scrolling`);
    }
    if (!(paneBody.scrollHeight > paneBody.clientHeight + tolerance)) {
      failures.push(
        `the pane body is ${paneBody.scrollHeight}px tall in ${paneBody.clientHeight}px - it has no room to scroll, so the fixture proves nothing`,
      );
    }
    if (paneBody.scrollTopAfter !== 100) {
      failures.push(`the pane body scrolled to ${paneBody.scrollTopAfter}px, expected 100px of internal scrolling`);
    }

    return failures.length === 0
      ? {
          pass: true,
          reason: `at ${m.viewport.width}px with a ${mode}: html overscroll-behavior is ${expectedOverscroll} on both axes, document scrolling stays available and unused, and the pane body scrolls ${paneBody.scrollTopAfter}px inside ${paneBody.clientHeight}px`,
        }
      : { pass: false, reason: failures.join("; ") };
  };
}
