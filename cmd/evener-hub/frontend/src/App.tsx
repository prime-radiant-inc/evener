import { type ComponentType, lazy, Suspense, useEffect } from "react";
import { syncThemeColor } from "./stores/prefs";

const WorkspaceRoute = lazy(() => import("./shell/AppShell").then((module) => ({ default: module.AppShell })));

// Dev-only routes, at /dev/widgets, /dev/surfaces, /dev/type and
// /dev/harness. import.meta.env.DEV
// keeps each lazy() import (and everything it pulls in) out of the graph
// entirely for a production build: `npm run build` emits no gallery/harness
// chunk (see this task's report for the dist/ listing that confirms it).
const WidgetGallery = import.meta.env.DEV ? lazy(() => import("./dev/WidgetGallery")) : null;
// SurfaceGallery mirrors WidgetGallery exactly, one route over: /dev/surfaces
// renders real pane-level surfaces (transcript, composer, chrome, rail, …)
// with fixture/seeded-store data instead of live network data - see
// dev/SurfaceGallery.tsx's own header.
const SurfaceGallery = import.meta.env.DEV ? lazy(() => import("./dev/SurfaceGallery")) : null;
// TypeSpecimen is the same shape again, at /dev/type: the type ramp, the
// leading, the eyebrow recipe, the rhythm steps and the two measures, all
// rendered from the real tokens so a ramp change is reviewed as a picture
// rather than a diff - see dev/TypeSpecimen.tsx's own header.
const TypeSpecimen = import.meta.env.DEV ? lazy(() => import("./dev/TypeSpecimen")) : null;
// PluginsMockups is the same dev-only shape again, at /dev/plugins-mockups:
// the plugins UI redesign mockups (settings lists, plugin detail sheet with
// contents, session launcher) rendered from the real widgets with fixture
// data - see dev/PluginsMockups.tsx's own header.
const PluginsMockups = import.meta.env.DEV ? lazy(() => import("./dev/PluginsMockups")) : null;
// Real session transcripts and composers at phone, tablet and desktop widths.
const ComposerResponsiveDemo = import.meta.env.DEV ? lazy(() => import("./dev/ComposerResponsiveDemo")) : null;
// DevHarness is a named export (dev/DevHarness.tsx), not a default one -
// React.lazy needs a Promise<{default}>, so this adapts the import rather
// than changing DevHarness's own export shape (which dev/DevHarness.test.tsx
// still imports directly by name).
const DevHarnessRoute = import.meta.env.DEV
  ? lazy(() => import("./dev/DevHarness").then((m) => ({ default: m.DevHarness })))
  : null;

// Every dev route is the same guard: a pathname, a lazy component, and a
// null component in production builds that matches nothing.
const devRoutes: ReadonlyArray<{ path: string; Component: ComponentType | null }> = [
  { path: "/dev/widgets", Component: WidgetGallery },
  { path: "/dev/surfaces", Component: SurfaceGallery },
  { path: "/dev/type", Component: TypeSpecimen },
  { path: "/dev/plugins-mockups", Component: PluginsMockups },
  { path: "/dev/composer-responsive", Component: ComposerResponsiveDemo },
  { path: "/dev/harness", Component: DevHarnessRoute },
];

export function App() {
  // The token sheet is loaded before this module (main.tsx's import order);
  // run the meta sync after mount so a dev-server stylesheet that arrives
  // late still gets the active page ground.
  useEffect(() => {
    syncThemeColor();
  }, []);
  const devRoute = devRoutes.find((route) => route.Component !== null && window.location.pathname === route.path);
  if (devRoute !== undefined && devRoute.Component !== null) {
    const DevComponent = devRoute.Component;
    return (
      <Suspense fallback={null}>
        <DevComponent />
      </Suspense>
    );
  }
  return (
    <Suspense fallback={null}>
      <WorkspaceRoute />
    </Suspense>
  );
}
