export default function assert({ rows: [active, quiet], guide, hostWidth, hostRoom }) {
  const within = (value, low, high) => value >= low && value <= high;
  if (
    !within(quiet.statusHeight, 1, quiet.height) ||
    !within(quiet.statusHeight - active.statusHeight, -1, 1) ||
    !within(quiet.height - active.height, -1, 1) ||
    !within(quiet.titleToMeta - active.titleToMeta, -1, 1)
  ) {
    return { pass: false, reason: `An empty status line changes the row rhythm: ${JSON.stringify({ active, quiet })}` };
  }
  if (guide !== "0px" || active.radius !== "0px" || active.background === quiet.background) {
    return { pass: false, reason: `Flat selection needs a tint, without a radius or guide: ${JSON.stringify({ active, quiet, guide })}` };
  }
  if (!within(hostWidth, 1, 280) || !within(hostRoom, -1, 280)) {
    return { pass: false, reason: `The remote host does not fit beside a long project name: ${JSON.stringify({ hostWidth, hostRoom })}` };
  }
  return { pass: true, reason: "the empty status line keeps the three-line rhythm and the selected row is flat" };
}
