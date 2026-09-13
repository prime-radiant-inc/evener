// kata zscn: a nested Dark gallery pane must win against a light ambient root
// at the custom-property boundary, not merely carry a data-theme marker.
// Palette: approved docs/superpowers/specs/2026-09-09-tufte-webui-design.md;
// retain both custom-property and actual painted-surface checks.
export default function assert(measurement) {
  const failures = [];
  if (measurement.darkSurface1 !== "#232320") {
    failures.push(`dark pane --surface-1 resolved to ${measurement.darkSurface1}, want #232320`);
  }
  if (measurement.darkPaneSurface0 !== "rgb(25, 25, 24)") {
    failures.push(`dark pane background resolved to ${measurement.darkPaneSurface0}, want dark --surface-0`);
  }
  if (measurement.darkProbeSurface1 !== "rgb(35, 35, 32)") {
    failures.push(`dark surface probe resolved to ${measurement.darkProbeSurface1}, want dark --surface-1`);
  }
  if (measurement.lightSurface1 !== "#FCFBF8" || measurement.lightProbeSurface1 !== "rgb(252, 251, 248)") {
    failures.push("light pane no longer resolves its own light --surface-1");
  }
  return failures.length === 0
    ? { pass: true, reason: "nested dark and light panes resolve their own surface tokens under a light root" }
    : { pass: false, reason: failures.join("; ") };
}
