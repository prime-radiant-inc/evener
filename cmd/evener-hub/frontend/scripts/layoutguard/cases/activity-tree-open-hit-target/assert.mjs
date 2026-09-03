export default function assert(measurement) {
  const failures = [];
  for (const fixture of measurement) {
    for (const edge of ["first", "last"]) {
      const sample = fixture[edge];
      if (
        Math.abs(sample.target.width - fixture.expectedTarget) > 0.5 ||
        Math.abs(sample.target.height - fixture.expectedTarget) > 0.5
      ) {
        failures.push(
          `${fixture.mode} ${edge}: Open target is ${sample.target.width.toFixed(1)}×${sample.target.height.toFixed(1)}, expected ${fixture.expectedTarget}×${fixture.expectedTarget}`,
        );
      }
      if (sample.row.height >= sample.target.height) {
        failures.push(
          `${fixture.mode} ${edge}: dense row ${sample.row.height.toFixed(1)}px is not shorter than the ${sample.target.height.toFixed(1)}px hit target; target growth affected leading`,
        );
      }
      const inset = 0.5;
      if (
        sample.target.top < fixture.scrollport.top + inset ||
        sample.target.bottom > fixture.scrollport.bottom - inset
      ) {
        failures.push(
          `${fixture.mode} ${edge}: complete Open target [${sample.target.top.toFixed(1)}, ${sample.target.bottom.toFixed(1)}] escapes scrollport [${fixture.scrollport.top.toFixed(1)}, ${fixture.scrollport.bottom.toFixed(1)}]`,
        );
      }
      const missed = sample.hits.filter((hit) => !hit.reachesButton);
      if (missed.length > 0) {
        failures.push(
          `${fixture.mode} ${edge}: ${missed.length}/2 center/exposed-edge elementFromPoint probes missed Open (${missed.map((hit) => `${hit.x.toFixed(1)},${hit.y.toFixed(1)}→${hit.hit ?? "null"}`).join(", ")})`,
        );
      }
    }
    if (fixture.treeOverflow.y !== "visible") {
      failures.push(
        `${fixture.mode}: .tree block-axis overflow computed to ${fixture.treeOverflow.y}, expected visible (inline clipping must not clip vertical target overhang)`,
      );
    }
  }
  if (failures.length > 0) return { pass: false, reason: failures.join("; ") };

  return {
    pass: true,
    reason: measurement
      .map(
        (f) =>
          `${f.mode}: complete first+last ${f.expectedTarget}px target rects stay inside the scrollport and 2/2 center/exposed-edge elementFromPoint probes hit each; rows ${f.first.row.height.toFixed(1)}/${f.last.row.height.toFixed(1)}px; inset ${f.scrollport.paddingTop.toFixed(1)}/${f.scrollport.paddingBottom.toFixed(1)}px; tree overflow-y ${f.treeOverflow.y}`,
      )
      .join(" | "),
  };
}
