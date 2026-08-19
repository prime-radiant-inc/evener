// read_file image read (ImageGallery size="large"): the image displays
// whole at up to 600px square. This case guards the geometry jsdom cannot
// see; the dropped "[image: ...]" header is jsdom-covered in
// fsTools.test.tsx.
//
// Three invariants, one per fixture:
//   1. WIDE (1200x800) renders 600x400 - capped by max-width alone, aspect
//      intact (a fixed square box would read 600x600 and fail).
//   2. TALL (400x1200) renders 200x600 - capped by max-height alone, so
//      neither declaration can cover for the other's deletion.
//   3. SMALL (100x50) renders a natural 100x50 - "up to" never upscales.
//
// NOT covered: the lightbox (jsdom, ImageGallery.test.tsx) and panes
// narrower than 600px (strip wrapping, not this cap).
const TOLERANCE = 0.5; // sub-pixel layout rounding, not a fudge factor

function near(actual, expected) {
  return Math.abs(actual - expected) <= TOLERANCE;
}

function describe(b) {
  return `${b.width.toFixed(1)}x${b.height.toFixed(1)}`;
}

export default function assert(m) {
  const expectations = [
    ["wide", m.wide, 600, 400, "max-width caps the width and the 3:2 aspect survives"],
    ["tall", m.tall, 200, 600, "max-height caps the height and the 1:3 aspect survives"],
    ["small", m.small, 100, 50, "an under-cap image keeps its natural size (up to 600px, never upscaled)"],
  ];
  for (const [name, box, w, h, why] of expectations) {
    if (!near(box.width, w) || !near(box.height, h)) {
      return {
        pass: false,
        reason: `the ${name} fixture renders at ${describe(box)}, expected ${w}x${h} - ${why}`,
      };
    }
  }
  return {
    pass: true,
    reason: `wide=${describe(m.wide)}, tall=${describe(m.tall)}, small=${describe(m.small)} - capped at 600px on the long axis, aspect preserved, never upscaled`,
  };
}
