// read_file image read (ImageGallery size="large", reached through
// toolRenderers.ts's outputImageSize): the image displays whole at up to
// 600px square. Before this, a read image rendered as the default 96px
// cover-crop thumbnail - the reader got a postage stamp of a screenshot the
// agent had just read, plus a "[image: png, N bytes]" header that is gone
// now too (that part is jsdom-covered in fsTools.test.tsx; this case guards
// the geometry jsdom cannot see).
//
// Three geometric invariants, one per fixture:
//
//   1. WIDE (1200x800 source) renders 600x400: max-width alone caps it, and
//      the aspect ratio survives the cap (a fixed square box would read
//      600x600; a cover crop would read 600x600 too - both fail here).
//   2. TALL (400x1200 source) renders 200x600: max-height alone caps it, so
//      neither declaration can cover for the other's deletion.
//   3. SMALL (100x50 source) renders a natural 100x50: "up to 600px" means
//      never upscale - a fixed 600px box fails this even with both max
//      declarations intact.
//
// What this case does NOT cover: the click-through lightbox (jsdom-covered
// in ImageGallery.test.tsx) and panes narrower than 600px (the strip's own
// wrapping, not the 600px cap).
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
