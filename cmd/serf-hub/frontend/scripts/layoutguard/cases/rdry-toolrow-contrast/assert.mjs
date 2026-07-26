// kata rdry: the demoted result line ("Wrote fizzbuzz.py") must clear WCAG AA
// (4.5:1, normal-size text) against the pane background it sits on, in BOTH
// themes - not just the one a screenshot happened to be taken in.
import { contrastRatio } from "../../contrast.mjs";

const AA_NORMAL_TEXT = 4.5;

export default function assert(measurement) {
  const failures = [];
  for (const theme of ["dark", "light"]) {
    const { fg, bg } = measurement[theme];
    const ratio = contrastRatio(fg, bg);
    if (ratio < AA_NORMAL_TEXT) {
      failures.push(
        `${theme} theme: ${ratio.toFixed(2)}:1 (fg=${fg} on bg=${bg}) - below the ${AA_NORMAL_TEXT}:1 AA floor`,
      );
    }
  }
  if (failures.length > 0) {
    return { pass: false, reason: failures.join("; ") };
  }
  const darkRatio = contrastRatio(measurement.dark.fg, measurement.dark.bg);
  const lightRatio = contrastRatio(measurement.light.fg, measurement.light.bg);
  return {
    pass: true,
    reason: `demoted line clears AA in both themes (dark ${darkRatio.toFixed(2)}:1, light ${lightRatio.toFixed(2)}:1)`,
  };
}
