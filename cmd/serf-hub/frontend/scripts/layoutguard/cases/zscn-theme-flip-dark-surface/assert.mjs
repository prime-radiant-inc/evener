// kata zscn: a nested Dark gallery pane must win against a light ambient root
// at the custom-property boundary, not merely carry a data-theme marker.
export default function assert(measurement) {
  const failures = [];
  if (measurement.darkSurface1 !== "#171E28") {
    failures.push(`dark pane --surface-1 resolved to ${measurement.darkSurface1}, want #171E28`);
  }
  if (measurement.darkPaneSurface0 !== "rgb(16, 21, 28)") {
    failures.push(`dark pane background resolved to ${measurement.darkPaneSurface0}, want dark --surface-0`);
  }
  if (measurement.darkProbeSurface1 !== "rgb(23, 30, 40)") {
    failures.push(`dark surface probe resolved to ${measurement.darkProbeSurface1}, want dark --surface-1`);
  }
  if (measurement.lightSurface1 !== "#FBFAF7" || measurement.lightProbeSurface1 !== "rgb(251, 250, 247)") {
    failures.push("light pane no longer resolves its own light --surface-1");
  }
  return failures.length === 0
    ? { pass: true, reason: "nested dark and light panes resolve their own surface tokens under a light root" }
    : { pass: false, reason: failures.join("; ") };
}
