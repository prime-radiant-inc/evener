// The phone Overview is transparent so its head and tab strip can be glass
// over the session transcript. Every region above the reading body must have
// a backing that blocks the transcript (an opaque background) or blurs it (a
// backdrop filter in regular Liquid Glass). In both themes and both glass
// modes, the head and the tab strip qualify, and the tab body stays an opaque
// reading surface. Before the fix the tab strip falls through to the
// transparent sidebar, so the transcript shows through it sharply.
function describe(chain) {
  return chain.map((entry) => `${entry.tag}.${entry.className}:${entry.backgroundColor}/${entry.backdropFilter}`).join(" <- ");
}

export default function assert(measurement) {
  const failures = [];
  const results = measurement?.results ?? [];

  if (results.length === 0) return { pass: false, reason: "harness produced no theme/glass measurements" };

  for (const result of results) {
    for (const region of ["head", "tabstrip"]) {
      const backing = result[region];
      if (!(backing.opaque || backing.blurred)) {
        failures.push(
          `${result.theme}/${result.glass}: ${region} has neither an opaque background nor a backdrop filter above it, so the transcript shows through it (${describe(backing.chain)})`,
        );
      }
    }
    if (!result.body.opaque) {
      failures.push(
        `${result.theme}/${result.glass}: the tab body is not an opaque reading surface (${describe(result.body.chain)})`,
      );
    }
  }

  return failures.length === 0
    ? {
        pass: true,
        reason: `head and tab strip block or blur the transcript, and the tab body stays opaque, in ${results.length} theme/glass combinations`,
      }
    : { pass: false, reason: failures.join("; ") };
}
