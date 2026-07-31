// kata edhz: the composer's settled attachment tile drew its image TWICE -
// <ImageGallery>, whose own 96px thumbnail button came along with the
// lightbox it had been imported for, plus a plain 80px <img> for the tile's
// own cover crop - as flex siblings in one 80x80 overflow:hidden box. Both
// overflowed it and their clipped crops met in a visible seam. Measured on
// the pre-fix CSS in this same harness: gallery thumb y=-20..78, plain img
// y=78..156, inside a tile spanning y=24..104.
//
// Three geometric invariants, none of which the old markup could satisfy:
//
//   1. The image fills the tile's content box EXACTLY. Two stacked images
//      each get part of the box, so neither one can.
//   2. Nothing inside the tile escapes the tile. The seam was two children
//      overflowing a clipping box; the same check also catches the remove
//      button, which used to hang at top/right -8px and lose its outer
//      corner to that same clip.
//   3. Pending and settled are the same box. The tile must not resize or
//      shift when the decode lands (kata 39xe's unification) - a jump under
//      the pointer is how you misclick a remove button.
// What this case does NOT cover: object-fit. Dropping `object-fit: cover`
// leaves every box in this measurement identical and only changes how the
// pixels inside the image box are scaled, so a geometric guard cannot see
// it - confirmed by mutation. Don't read a pass here as covering it.
const TOLERANCE = 0.5; // sub-pixel layout rounding, not a fudge factor

function fillsExactly(image, content) {
  return (
    Math.abs(image.left - content.left) <= TOLERANCE &&
    Math.abs(image.top - content.top) <= TOLERANCE &&
    Math.abs(image.right - content.right) <= TOLERANCE &&
    Math.abs(image.bottom - content.bottom) <= TOLERANCE
  );
}

function escapees(state) {
  return state.children.filter(
    (child) =>
      child.left < state.tile.left - TOLERANCE ||
      child.top < state.tile.top - TOLERANCE ||
      child.right > state.tile.right + TOLERANCE ||
      child.bottom > state.tile.bottom + TOLERANCE,
  );
}

function describe(b) {
  return `${b.left.toFixed(1)},${b.top.toFixed(1)} ${b.width.toFixed(1)}x${b.height.toFixed(1)}`;
}

export default function assert(m) {
  if (m.settled.overflow !== "hidden") {
    return {
      pass: false,
      reason: `the tile's overflow is "${m.settled.overflow}", not hidden - this case's containment checks assume the tile clips, and an unclipped tile fails differently (children bleed over neighbours instead of being cut)`,
    };
  }
  for (const [name, state] of [
    ["settled", m.settled],
    ["pending", m.pending],
  ]) {
    if (!fillsExactly(state.image, state.tileContent)) {
      return {
        pass: false,
        reason: `the ${name} tile's image box (${describe(state.image)}) does not fill its content box (left=${state.tileContent.left.toFixed(1)} top=${state.tileContent.top.toFixed(1)} right=${state.tileContent.right.toFixed(1)} bottom=${state.tileContent.bottom.toFixed(1)}) - something else is taking part of the tile, which is how two stacked images produced a seam (kata edhz)`,
      };
    }
    const escaped = escapees(state);
    if (escaped.length > 0) {
      return {
        pass: false,
        reason: `${escaped.length} element(s) inside the ${name} tile escape it and are clipped away: ${escaped.map((c) => `#${c.id} (${describe(c)})`).join(", ")} - tile is ${describe(state.tile)}`,
      };
    }
  }
  const drift = Math.max(
    Math.abs(m.pending.tile.width - m.settled.tile.width),
    Math.abs(m.pending.tile.height - m.settled.tile.height),
  );
  if (drift > TOLERANCE) {
    return {
      pass: false,
      reason: `the pending tile (${describe(m.pending.tile)}) is not the same box as the settled one (${describe(m.settled.tile)}) - the tile would resize by ${drift.toFixed(1)}px when the decode lands`,
    };
  }
  return {
    pass: true,
    reason: `one image fills the ${m.settled.tile.width.toFixed(0)}x${m.settled.tile.height.toFixed(0)} tile exactly in both states, nothing inside it is clipped, and pending and settled are the same box`,
  };
}
