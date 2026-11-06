// kata zscn: a nested Dark gallery pane must win against a light ambient root
// at the custom-property boundary, not merely carry a data-theme marker.
// Palette: approved docs/superpowers/specs/2026-09-09-tufte-webui-design.md;
// retain both custom-property and actual painted-surface checks.
export default function assert(measurement) {
  const failures = [];
  if (measurement.darkSurface1 !== "#201F1C") {
    failures.push(`dark pane --surface-1 resolved to ${measurement.darkSurface1}, want #201F1C`);
  }
  if (measurement.darkPaneSurface0 !== "rgb(20, 20, 18)") {
    failures.push(`dark pane background resolved to ${measurement.darkPaneSurface0}, want dark --surface-0`);
  }
  if (measurement.darkProbeSurface1 !== "rgb(32, 31, 28)") {
    failures.push(`dark surface probe resolved to ${measurement.darkProbeSurface1}, want dark --surface-1`);
  }
  if (measurement.lightSurface1 !== "#FBF8F0" || measurement.lightProbeSurface1 !== "rgb(251, 248, 240)") {
    failures.push("light pane no longer resolves its own light --surface-1");
  }
  return failures.length === 0
    ? { pass: true, reason: "nested dark and light panes resolve their own surface tokens under a light root" }
    : { pass: false, reason: failures.join("; ") };
}
