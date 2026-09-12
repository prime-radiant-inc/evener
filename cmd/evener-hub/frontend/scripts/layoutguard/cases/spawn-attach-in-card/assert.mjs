// Issue #198 / the rejected PR #242: on a phone the spawn pane's attach button
// was stranded at the bottom of the VIEWPORT, nowhere near the prompt it
// attaches to. Spawn passed PromptCard a controlsClassName that
// spawn.module.css turned into `position: fixed; bottom: 0` inside its 899px
// media block, so the card's own control row detached from the card and became
// a page-level band. Measured in this same harness on the pre-fix CSS at
// 390x844: the card is 12,12 366x106 while the control row is 0,767 390x77,
// with the attach button at 16,784 44x44 - two thirds of a phone screen away
// from the prompt it attaches to.
//
// The session composer has never had this problem - it keeps attach, the model
// trigger and the send verb inside the card's control row at every width
// (Composer.tsx's leading/actions slots) - so the fix is to render the same
// shape, and this case is what pins it there.
//
// Four geometric claims, and one placement claim aimed at the OTHER way this
// has been got wrong:
//
//   1. The control row is not a fixed band. `position: fixed` is the
//      mechanism; naming it directly means a reintroduction fails here for
//      the reason it is wrong rather than only through its symptoms.
//   2. Everything in the row - the row itself, attach, the model trigger,
//      Start - is inside the card. That is the containment the fixed band
//      broke, and it is what "reuse the composer's UI" means geometrically.
//   3. The model trigger is actually on screen at phone width. It is the
//      whole of issue #198's second half (setting the model from the card),
//      and a display:none typo would otherwise pass claim 2 vacuously - a box
//      with no size is trivially "contained".
//   4. Attach sits BELOW the writing surface, not on top of it. PR #242's
//      first attempt overlaid the paperclip on the textarea's lower-right
//      corner, where typed text runs underneath it. The composer puts it in
//      the row under the field; so does this.
const TOLERANCE = 0.5; // sub-pixel layout rounding, not a fudge factor

function describe(b) {
  if (b === null) return "missing";
  return `${b.left.toFixed(1)},${b.top.toFixed(1)} ${b.width.toFixed(1)}x${b.height.toFixed(1)}`;
}

function escapes(child, parent) {
  return (
    child.left < parent.left - TOLERANCE ||
    child.top < parent.top - TOLERANCE ||
    child.right > parent.right + TOLERANCE ||
    child.bottom > parent.bottom + TOLERANCE
  );
}

export default function assert(m) {
  const failures = [];
  const missing = ["card", "field", "controls", "attach", "start", "modelTrigger", "modelValue", "effort"].filter(
    (k) => m[k] === null,
  );
  if (missing.length > 0) {
    return {
      pass: false,
      reason: `the harness is missing ${missing.join(", ")} - nothing else can be measured without them`,
    };
  }

  if (m.controls.position === "fixed") {
    failures.push(
      `the control row is position: fixed (${describe(m.controls)}) - it is a viewport band, not the card's own row`,
    );
  }
  for (const [name, box] of [
    ["the control row", m.controls],
    ["the attach button", m.attach],
    ["the model trigger", m.modelTrigger],
    ["the model value", m.modelValue],
    ["the effort control", m.effort],
    ["the Start button", m.start],
  ]) {
    if (escapes(box, m.card)) {
      failures.push(`${name} (${describe(box)}) is outside the prompt card (${describe(m.card)})`);
    }
  }
  if (m.modelGateDisplay === "none" || m.modelTrigger.width <= 1 || m.modelTrigger.height <= 1) {
    failures.push(
      `the model trigger is not on screen at ${m.viewport.width}px (display: ${m.modelGateDisplay}, box ${describe(m.modelTrigger)}) - the card is where a phone sets the model`,
    );
  }
  if (m.attach.top < m.field.bottom - TOLERANCE) {
    failures.push(
      `the attach button (${describe(m.attach)}) overlaps the writing surface (${describe(m.field)}) instead of sitting in the row beneath it`,
    );
  }
  // Composer unification: the ~100-char qualified model id must ellipsize
  // inside the row (content past its box) rather than push effort/Start out,
  // and the effort trigger must keep the 44px phone tap floor.
  if (m.modelValue.scrollWidth <= m.modelValue.clientWidth + 1) {
    failures.push(
      `the long model id is not ellipsizing (scroll ${m.modelValue.scrollWidth}px vs client ${m.modelValue.clientWidth}px) - it renders at full min-content width`,
    );
  }
  if (m.effort.height + TOLERANCE < 44) {
    failures.push(`the effort control is ${m.effort.height}px tall, below the 44px phone tap floor`);
  }

  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };
  return {
    pass: true,
    reason: `at ${m.viewport.width}x${m.viewport.height} the control row is ${m.controls.position}, and attach (${describe(m.attach)}), the model trigger (${describe(m.modelTrigger)}) and Start (${describe(m.start)}) all sit inside the card (${describe(m.card)}) below the field`,
  };
}
