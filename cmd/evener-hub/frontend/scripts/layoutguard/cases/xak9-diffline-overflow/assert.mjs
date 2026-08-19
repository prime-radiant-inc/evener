// kata xak9: a diff line longer than its box must stop at the box edge WITH
// a visible signal (an ellipsis) - never escape it silently. Primary
// assertion mirrors p6g8's own containment check: the content span's
// rendered box must stay inside its .root container. Secondary: the
// declared text-overflow must actually be "ellipsis", not merely "hidden"
// (which clips with no signal at all - the exact defect being fixed).
export default function assert(measurement) {
  const overflow = measurement.contentOverflowsRoot;
  if (overflow > 1) {
    return {
      pass: false,
      reason: `diff line content overflows its .root container by ${overflow.toFixed(1)}px (content.right=${measurement.content.right.toFixed(1)}, root.right=${measurement.root.right.toFixed(1)}) - silent clipping, kata xak9`,
    };
  }
  if (measurement.textOverflow !== "ellipsis") {
    return {
      pass: false,
      reason: `diff line content fits its container but text-overflow is "${measurement.textOverflow}", not "ellipsis" - a fit achieved by hard clipping gives no signal that content is missing`,
    };
  }
  return {
    pass: true,
    reason: `content fits inside .root with ${(-overflow).toFixed(1)}px to spare and truncates with a visible ellipsis`,
  };
}
