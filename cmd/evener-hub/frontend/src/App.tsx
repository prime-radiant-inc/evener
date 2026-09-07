import { lazy, Suspense } from "react";
import { AppShell } from "./shell/AppShell";

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
// DevHarness is a named export (dev/DevHarness.tsx), not a default one -
// React.lazy needs a Promise<{default}>, so this adapts the import rather
// than changing DevHarness's own export shape (which dev/DevHarness.test.tsx
// still imports directly by name).
const DevHarnessRoute = import.meta.env.DEV
  ? lazy(() => import("./dev/DevHarness").then((m) => ({ default: m.DevHarness })))
  : null;

export function App() {
  if (WidgetGallery !== null && window.location.pathname === "/dev/widgets") {
    return (
      <Suspense fallback={null}>
        <WidgetGallery />
      </Suspense>
    );
  }
  if (SurfaceGallery !== null && window.location.pathname === "/dev/surfaces") {
    return (
      <Suspense fallback={null}>
        <SurfaceGallery />
      </Suspense>
    );
  }
  if (TypeSpecimen !== null && window.location.pathname === "/dev/type") {
    return (
      <Suspense fallback={null}>
        <TypeSpecimen />
      </Suspense>
    );
  }
  if (DevHarnessRoute !== null && window.location.pathname === "/dev/harness") {
    return (
      <Suspense fallback={null}>
        <DevHarnessRoute />
      </Suspense>
    );
  }
  return <AppShell />;
}
