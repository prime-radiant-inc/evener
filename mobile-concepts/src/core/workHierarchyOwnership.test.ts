import { describe, expect, it } from "vitest";
import constellationWorkView from "../concepts/constellation/WorkView.tsx?raw";
import fieldNotesWorkView from "../concepts/field-notes/WorkView.tsx?raw";
import stillwaterWorkView from "../concepts/stillwater/WorkView.tsx?raw";

const workViews = [
  { path: "concepts/stillwater/WorkView.tsx", source: stillwaterWorkView },
  {
    path: "concepts/constellation/WorkView.tsx",
    source: constellationWorkView,
  },
  { path: "concepts/field-notes/WorkView.tsx", source: fieldNotesWorkView },
] as const;

describe("Work hierarchy ownership", () => {
  it.each(workViews)(
    "keeps $path on the shared hierarchy derivation",
    ({ source }) => {
      expect(source).toContain("buildWorkHierarchy,");
      expect(source).toContain('from "../../core/selectors";');
      expect(source).toContain("buildWorkHierarchy(nodes)");
      expect(source).not.toMatch(/function\s+buildWorkHierarchy\b/);
    },
  );
});
