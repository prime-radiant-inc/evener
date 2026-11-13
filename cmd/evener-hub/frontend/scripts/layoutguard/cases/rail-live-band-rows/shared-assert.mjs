// The Live band rows' second line holds three contracts:
//
// 1. The why line never wraps: however long its text, the box stays exactly
//    one caption line tall (the height a short why line measures) and its
//    right edge stays inside the row, truncating with an ellipsis instead.
// 2. A row with a why line and a row without start their title text at the
//    same x under the same band.
// 3. The why line's left edge is the title text's left edge: it sits under
//    the title, not under the status dot.
//
// Every comparison is two-sided and false on NaN. 0.5px tolerance clears
// sub-pixel rounding only.
const TOL = 0.5;

const within = (a, b, tol) => Math.abs(a - b) <= tol;

export default function assert(m) {
  const { why, whyShort, whyOverflows, row, whyLabel, quietLabel } = m;

  if (!(whyShort.height >= 8 && whyShort.height <= 24)) {
    return { pass: false, reason: `reference why line is ${whyShort.height}px tall, not one caption line` };
  }
  if (!within(why.height, whyShort.height, TOL)) {
    return {
      pass: false,
      reason: `long why line is ${why.height}px tall against ${whyShort.height}px for one line - it wrapped`,
    };
  }
  if (!(why.right <= row.right + TOL && why.right >= row.left)) {
    return {
      pass: false,
      reason: `why line's right edge ${why.right} is outside its row [${row.left}, ${row.right}]`,
    };
  }
  if (whyOverflows !== true) {
    return { pass: false, reason: "long why text does not overflow its box - the fixture no longer exercises truncation" };
  }

  const titleDrift = whyLabel.left - quietLabel.left;
  if (!within(whyLabel.left, quietLabel.left, TOL)) {
    return {
      pass: false,
      reason: `title with a why line starts ${titleDrift.toFixed(1)}px from one without (${whyLabel.left} vs ${quietLabel.left})`,
    };
  }

  const whyDrift = why.left - whyLabel.left;
  if (!within(why.left, whyLabel.left, TOL)) {
    return {
      pass: false,
      reason: `why line starts ${whyDrift.toFixed(1)}px from the title text (${why.left} vs ${whyLabel.left})`,
    };
  }

  return {
    pass: true,
    reason: `why line stays one ${why.height}px line inside its row (right=${why.right.toFixed(1)}/${row.right.toFixed(1)}), titles align (${titleDrift.toFixed(1)}px drift) and the why line sits ${whyDrift.toFixed(1)}px from the title x`,
  };
}
