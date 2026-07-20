import { lazy, Suspense } from "react";

// Dev-only widget gallery, at /dev/widgets. import.meta.env.DEV keeps the
// lazy() import (and everything it pulls in) out of the graph entirely for
// a production build: `npm run build` emits no gallery chunk (see this
// task's report for the dist/ listing that confirms it).
const WidgetGallery = import.meta.env.DEV ? lazy(() => import("./dev/WidgetGallery")) : null;

export function App() {
  if (WidgetGallery !== null && window.location.pathname === "/dev/widgets") {
    return (
      <Suspense fallback={null}>
        <WidgetGallery />
      </Suspense>
    );
  }
  return <main>serf workspace shell — wave 1</main>;
}
