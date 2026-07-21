import { lazy, Suspense } from "react";
import { AppShell } from "./shell/AppShell";

// Dev-only routes, at /dev/widgets and /dev/harness. import.meta.env.DEV
// keeps each lazy() import (and everything it pulls in) out of the graph
// entirely for a production build: `npm run build` emits no gallery/harness
// chunk (see this task's report for the dist/ listing that confirms it).
const WidgetGallery = import.meta.env.DEV ? lazy(() => import("./dev/WidgetGallery")) : null;
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
  if (DevHarnessRoute !== null && window.location.pathname === "/dev/harness") {
    return (
      <Suspense fallback={null}>
        <DevHarnessRoute />
      </Suspense>
    );
  }
  return <AppShell />;
}
