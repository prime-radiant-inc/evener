// Primary assertion: the button's label must fit INSIDE its fixed-height
// box - a wrapped label has nowhere to go but out of it (the sizes pin
// height: 24/28/32px), which is exactly what the credentials header showed.
// This is the width-independent invariant that button.module.css's
// `.button { white-space: nowrap }` exists to guarantee: nowrap makes the
// label's min-content its full width, so the flex row squeezes the narrative
// sibling instead of the button.
export default function assert(measurement) {
  const overflow = measurement.verticalOverflow;
  if (overflow > 1) {
    // >1px, not >0, to stay clear of sub-pixel layout rounding noise.
    return {
      pass: false,
      reason: `button label overflows its fixed-height box vertically by ${overflow.toFixed(1)}px (scrollHeight=${measurement.scrollHeight}, clientHeight=${measurement.clientHeight}) - the label wrapped; button.module.css's .button is missing white-space:nowrap`,
    };
  }
  return {
    pass: true,
    reason: `label stays on one line inside the ${measurement.box.height.toFixed(1)}px-tall box (horizontal overflow: ${measurement.horizontalOverflow.toFixed(1)}px)`,
  };
}
