// kata hk8v: the composer's staged-attachment remove button is opacity:0 at
// rest and revealed by hovering its tile. Keyboard users never hover, so
// focus has to reveal it too - otherwise tabbing lands on a control that is
// there, clickable, and invisible.
//
// Three assertions, in the order they'd tell you what broke:
//   - resting stays hidden (if it doesn't, the premise is gone and the other
//     two prove nothing - a permanently visible button trivially "reveals")
//   - hover still reveals, so a focus fix that broke the hover rule is caught
//   - focus reveals AND draws the accent ring, matching chip.module.css's own
//     .remove:focus-visible
export default function assert(m) {
  if (m.resting.opacity !== 0) {
    return {
      pass: false,
      reason: `the remove button is opacity ${m.resting.opacity} at rest, not 0 - this case's premise (a hover-revealed control) no longer holds, so its focus assertions prove nothing`,
    };
  }
  if (m.hovered.opacity !== 1) {
    return {
      pass: false,
      reason: `hovering the tile leaves the remove button at opacity ${m.hovered.opacity}, not 1 - the hover reveal regressed`,
    };
  }
  if (m.focused.opacity !== 1) {
    return {
      pass: false,
      reason: `the remove button under :focus-visible is opacity ${m.focused.opacity}, not 1 - a keyboard user focuses an invisible control (kata hk8v)`,
    };
  }
  const width = Number.parseFloat(m.focused.outlineWidth);
  if (m.focused.outlineStyle === "none" || !(width > 0)) {
    return {
      pass: false,
      reason: `the remove button under :focus-visible draws no outline (style=${m.focused.outlineStyle}, width=${m.focused.outlineWidth}) - it is visible but carries no focus ring`,
    };
  }
  if (m.focused.outlineColor !== m.accent) {
    return {
      pass: false,
      reason: `the focus ring is ${m.focused.outlineColor}, not the accent ${m.accent} that chip.module.css's own .remove:focus-visible uses`,
    };
  }
  return {
    pass: true,
    reason: `remove button is opacity 0 at rest, 1 on tile hover, and 1 under :focus-visible with a ${m.focused.outlineWidth} ${m.focused.outlineStyle} ${m.focused.outlineColor} ring`,
  };
}
