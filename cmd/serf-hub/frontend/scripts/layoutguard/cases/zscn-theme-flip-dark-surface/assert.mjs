// kata zscn: a nested Dark gallery pane must win against a light ambient root
// at the custom-property boundary, not merely carry a data-theme marker.
export default function assert(measurement) {
  const failures = [];
  if (measurement.darkSurface1 !== "#161B22") {
    failures.push(`dark pane --surface-1 resolved to ${measurement.darkSurface1}, want #161B22`);
  }
  if (measurement.darkPaneSurface0 !== "rgb(14, 17, 22)") {
    failures.push(`dark pane background resolved to ${measurement.darkPaneSurface0}, want dark --surface-0`);
  }
  if (measurement.darkProbeSurface1 !== "rgb(22, 27, 34)") {
    failures.push(`dark surface probe resolved to ${measurement.darkProbeSurface1}, want dark --surface-1`);
  }
  if (measurement.lightSurface1 !== "#FFFFFF" || measurement.lightProbeSurface1 !== "rgb(255, 255, 255)") {
    failures.push("light pane no longer resolves its own light --surface-1");
  }
  return failures.length === 0
    ? { pass: true, reason: "nested dark and light panes resolve their own surface tokens under a light root" }
    : { pass: false, reason: failures.join("; ") };
}
