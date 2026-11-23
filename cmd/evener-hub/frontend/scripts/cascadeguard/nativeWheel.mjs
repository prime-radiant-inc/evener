const WHEEL_STEP = 900;

// `readInsideTallEntry` repositions the source reader with a native wheel
// before measuring interruption and reflow behavior. An upward wheel needs a
// full step of room to apply; a reader closer to the top than that clamps at
// the boundary and leaves the reading line at the entry's edge. So a
// repositioning wheel always points at the end that has room.
export function readerWheelDelta({ scrollTop }) {
  return scrollTop < WHEEL_STEP ? WHEEL_STEP : -WHEEL_STEP;
}
