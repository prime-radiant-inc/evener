// Issue #197's mechanism and its fix, both directions, at three phone widths:
//
// - The 200-rune resume-candidate title must not inflate the welcome .actions
//   column past the pane body. When it does, PaneScaffold's .body
//   overflow-x:clip slices the centered button symmetrically into a
//   marker-less mid-string fragment (leftClippedPx/rightClippedPx both
//   positive), and .examples' width:min(100%,36rem) resolves its 100% against
//   the inflated column, stretching every suggestion button past the viewport.
// - The fix (white-space:normal on the resume button) must not reintroduce
//   the bug button.module.css's nowrap exists to prevent: a wrapped label
//   spilling out of a fixed-height box (scrollHeight > clientHeight).
// - The .examples buttons keep their deliberate text-align:left
//   (welcome.module.css's own .examples button rule) - an .actions-scoped
//   override written after it, at equal specificity, silently recenters them.
const TOLERANCE = 1;

export default function assert(measurements) {
  const failures = [];

  for (const m of measurements) {
    const tag = `${m.width}px ${m.candidate ? "long-candidate" : "no-candidate"}`;
    const bodyWidth = m.body.rect.width;

    const contained = (name, box) => {
      if (box.leftClippedPx > TOLERANCE || box.rightClippedPx > TOLERANCE) {
        failures.push(
          `${tag}: ${name} escapes the pane body (left ${box.leftClippedPx.toFixed(1)}px, right ${box.rightClippedPx.toFixed(1)}px past its clip edges; button ${box.rect.width.toFixed(1)}px wide in a ${bodyWidth.toFixed(1)}px body)`,
        );
      }
    };
    const fitsVertically = (name, box) => {
      const spill = box.scrollHeight - box.clientHeight;
      if (spill > TOLERANCE) {
        failures.push(
          `${tag}: ${name}'s wrapped label spills ${spill}px out of its fixed-height box (scrollHeight=${box.scrollHeight}, clientHeight=${box.clientHeight}) - it needs height:auto alongside white-space:normal`,
        );
      }
    };

    if (m.candidate) {
      if (!m.resume) {
        failures.push(`${tag}: resume button missing from fixture`);
      } else {
        contained("resume button", m.resume);
        fitsVertically("resume button", m.resume);
      }
    }

    contained("New session button", m.newSession);

    if (m.examples.length !== 3) failures.push(`${tag}: expected 3 example buttons, found ${m.examples.length}`);
    m.examples.forEach((box, i) => {
      contained(`example button ${i + 1}`, box);
      fitsVertically(`example button ${i + 1}`, box);
      if (box.textAlign !== "left") {
        failures.push(
          `${tag}: example button ${i + 1} computed text-align is ${box.textAlign}, expected left (welcome.module.css's .examples button rule was overridden)`,
        );
      }
    });

    for (const [name, rect] of [
      ["welcome .actions column", m.actions],
      [".examples container", m.examplesDiv],
      [".hints container", m.hints],
    ]) {
      if (rect.width > bodyWidth + TOLERANCE) {
        failures.push(`${tag}: ${name} is ${rect.width.toFixed(1)}px wide inside a ${bodyWidth.toFixed(1)}px pane body`);
      }
    }
  }

  return failures.length === 0
    ? {
        pass: true,
        reason: `resume button, suggestions, and hints stay inside the pane body at ${measurements.map((m) => m.width).filter((w, i, a) => a.indexOf(w) === i).join("/")}px, the wrapped title fits its box, and the example buttons stay left-aligned`,
      }
    : { pass: false, reason: failures.join("; ") };
}
