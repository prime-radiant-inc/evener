// Section discovery shared by the two dev galleries: each renders one eager
// directory of section modules, sorted by path so the page order is stable
// across builds. The globs stay literal here because Vite only transforms a
// call it can read statically; the exclusion pattern keeps a colocated
// *.test.tsx out of the glob, since a test module with no default export
// would render as `undefined`.
import type { ComponentType } from "react";

interface GallerySectionModule {
  default: ComponentType;
}

export interface GallerySectionEntry {
  path: string;
  Section: ComponentType;
}

function discoverSections(
  modules: Record<string, GallerySectionModule | undefined>,
  kind: string,
): GallerySectionEntry[] {
  return Object.keys(modules)
    .sort()
    .map((path) => {
      // path came from Object.keys(modules) itself, so the lookup always hits
      // - but that's this loop's own invariant, invisible to the indexed-
      // access type. Checked, not asserted.
      const mod = modules[path];
      if (!mod) throw new Error(`missing ${kind} module for ${path}`);
      return { path, Section: mod.default };
    });
}

export const WIDGET_GALLERY_SECTIONS = discoverSections(
  import.meta.glob<GallerySectionModule>(["./gallery-sections/*.tsx", "!./gallery-sections/*.test.tsx"], {
    eager: true,
  }),
  "gallery section",
);

export const SURFACE_GALLERY_SECTIONS = discoverSections(
  import.meta.glob<GallerySectionModule>(["./surface-sections/*.tsx", "!./surface-sections/*.test.tsx"], {
    eager: true,
  }),
  "surface gallery section",
);
