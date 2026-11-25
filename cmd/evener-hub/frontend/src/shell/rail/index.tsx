// Controller-owned mount point: AppShell imports { RailHost } from here and
// renders it as a sibling of DockHost (and inside StackHost's railSlot on
// mobile). The real host lives in ./RailHost - this barrel owns only the lazy
// seam in front of it.
//
// RailHost (Rail 1605 lines + RailRow + railNodes + navigation selectors)
// stays out of AppShell's initial chunk behind this seam: the rail is
// below-the-fold chrome beside the pane workspace, never the first-paint
// path. The Suspense fallback is null because AppShell's own content region
// already paints the workspace behind it; RailHost.test.tsx keeps importing
// ./RailHost directly, so its sync assertions never see this boundary.
//
// A rejected chunk lands on RailChunkBoundary below (the DockRegion
// chunk-boundary pattern), scoped to the rail: without it the lazy() rethrow
// would unmount the whole shell, which has no boundary of its own. Retry
// swaps in a fresh lazy component over loadRailHost's cache-busted URL -
// Chrome retains a failed module fetch by URL (see railHostChunk.ts), so
// re-rendering the old one, or fetching the same URL again, could never
// recover.
//
// There is deliberately no `export { Rail }` here: a value re-export would
// keep an eager edge from this barrel to the 1605-line Rail module inside
// AppShell's initial chunk, defeating the split. Importers take ./Rail (or
// ./RailHost) directly.

import { type CSSProperties, type JSX, type LazyExoticComponent, lazy, Suspense, useEffect } from "react";
import { usePrefsStore } from "../../stores/prefs";
import { Button, EmptyState } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import {
  type ChunkFailureFallbackProps,
  createChunkFailureBoundary,
  createLazyChunkStore,
  useChunkRetry,
} from "../chunkFailureBoundary";
import { reloadPage } from "../pageReload";
import styles from "./Rail.module.css";
import { RAIL_WIDTH_PROPERTY } from "./RailResizeHandle";
import { noteRailWrapperMounted, noteRailWrapperUnmounted } from "./railController";
import { useRailHeaderAction } from "./railHeaderAction";
import { isStaleRailHostChunkError, loadRailHost } from "./railHostChunk";

const CLASS = {
  failureShell: requireClass(styles.rail, "Rail.module.css", "rail"),
  fallbackHeader: requireClass(styles.fallbackHeader, "Rail.module.css", "fallbackHeader"),
};

// The failure state stands exactly where the rail stood: the same .rail
// flex item (authoritative flex:none width, background, right border) with
// the dragged sidebarWidth pushed in as Rail's own --rail-width, so the
// desktop content row keeps its width and styling and the workspace beside
// it never stretches into the gap. A bare EmptyState here would leave the
// row's only other child - the flex:1 workspace - to fill the window.
function RailFailure({ failure, retry, canReload }: ChunkFailureFallbackProps): JSX.Element {
  const sidebarWidth = usePrefsStore((s) => s.sidebarWidth);
  const headerAction = useRailHeaderAction();
  return (
    <div
      className={CLASS.failureShell}
      style={{ [RAIL_WIDTH_PROPERTY]: `${sidebarWidth}px` } as CSSProperties}
      data-testid="rail-chunk-failure"
    >
      {headerAction !== null && <div className={CLASS.fallbackHeader}>{headerAction}</div>}
      <EmptyState
        title="Couldn't load the sidebar"
        hint={failure}
        action={
          <>
            <Button size="sm" onClick={retry}>
              Retry
            </Button>
            {canReload && (
              <Button size="sm" variant="quiet" onClick={reloadPage}>
                Reload page
              </Button>
            )}
          </>
        }
      />
    </div>
  );
}

function RailLoading(): JSX.Element | null {
  const headerAction = useRailHeaderAction();
  if (headerAction === null) return null;
  return (
    <div className={CLASS.failureShell} data-testid="rail-chunk-loading">
      <div className={CLASS.fallbackHeader}>{headerAction}</div>
    </div>
  );
}

type RailHostComponent = () => JSX.Element;
type RailHostChunk = LazyExoticComponent<RailHostComponent>;

function lazyRailHost(cacheBust = false): RailHostChunk {
  // RailHost is a named export, so the import() promise is adapted the same
  // way App.tsx's own DevHarnessRoute does for dev/DevHarness.tsx.
  return lazy(() => loadRailHost(cacheBust).then((m) => ({ default: m.RailHost })));
}

// One shared payload: the chunk is fetched once per page load and every later
// mount of this host (a breakpoint crossing, a drawer open/close) renders
// RailHost straight away instead of suspending again.
const railHost = createLazyChunkStore(lazyRailHost);

// A payload caches its outcome for the life of the module, success or
// failure, so one test's failed chunk would otherwise be every later test's
// failed chunk. Mirrors the resetXForTests precedent every other module
// singleton here follows (stores/navigation/store.ts's own note); no
// production code should ever call it.
export function resetRailChunkForTests(): void {
  railHost.getState().publish(lazyRailHost());
}

const RailChunkBoundary = createChunkFailureBoundary(isStaleRailHostChunkError);

export function RailHost(_props: { railSlot?: never } = {}): JSX.Element {
  // A retry needs a NEW lazy component, not a re-render of the old one:
  // React.lazy stores the rejection on its payload and rethrows that same
  // error on every subsequent render, forever.
  const { Chunk: Host, retry, reloadAvailable } = useChunkRetry(railHost, lazyRailHost);
  // The wrapper's mount lifetime is the controller's only signal that a
  // handler is on its way: a reveal fired while this wrapper is mounted but
  // the chunk (and with it RailHost's handler registration) has not arrived
  // yet queues for the next handler. Unmounting clears the wait, so a
  // reveal can never leak onto a later, unrelated mount.
  useEffect(() => {
    noteRailWrapperMounted();
    return () => noteRailWrapperUnmounted();
  }, []);

  return (
    <RailChunkBoundary
      onRetry={retry}
      reloadAvailable={reloadAvailable}
      renderFailure={(state) => <RailFailure {...state} />}
    >
      <Suspense fallback={<RailLoading />}>
        <Host />
      </Suspense>
    </RailChunkBoundary>
  );
}
