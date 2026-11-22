// The dock half of the shell's workspace area: the lazily-fetched DockHost,
// plus the boundary that keeps its chunk's failure inside this region.
//
// AppShell mounts this where it used to mount <Suspense><DockHost/></Suspense>
// directly. Without a boundary, a chunk that 4xx/5xx's (a hub restarting
// mid-load, a deploy that replaced the hashed filename) makes React.lazy
// rethrow during render with no boundary anywhere above it - React unmounts
// the whole tree and the user gets a white page (kata 1s47). Scoped here, the
// same failure costs the workspace and nothing else: the rail, the connection
// banner, the toasts and the command palette all stay up.

import { type LazyExoticComponent, lazy, type ReactNode, Suspense, useCallback } from "react";
import { cancelConversationHandoff, conversationHandoffGeneration } from "../stores/conversationClaimHandoff";
import { Button, EmptyState, Loader } from "../widgets";
import {
  type ChunkFailureFallbackProps,
  createChunkFailureBoundary,
  createLazyChunkStore,
  useChunkRetry,
} from "./chunkFailureBoundary";
import styles from "./DockRegion.module.css";
import { isStaleDockHostChunkError, loadDockHost } from "./dockHostChunk";
import { reloadPage } from "./pageReload";

function DockFailure({ failure, retry, reloadAvailable, isStaleFailure }: ChunkFailureFallbackProps): ReactNode {
  return (
    <EmptyState
      title="Couldn't load the workspace"
      hint={failure}
      action={
        <>
          <Button size="sm" onClick={retry}>
            Retry
          </Button>
          {reloadAvailable && isStaleFailure(failure) && (
            <Button size="sm" variant="quiet" onClick={reloadPage}>
              Reload page
            </Button>
          )}
        </>
      }
    />
  );
}

type DockHostComponent = () => ReactNode;
type DockHostChunk = LazyExoticComponent<DockHostComponent>;

function lazyDockHost(cacheBust = false): DockHostChunk {
  // DockHost is a named export, so the import() promise is adapted the same
  // way App.tsx's own DevHarnessRoute does for dev/DevHarness.tsx.
  return lazy(() => loadDockHost(cacheBust).then((m) => ({ default: m.DockHost })));
}

// One shared payload: the chunk is fetched once per page load and every later
// mount of this region (a breakpoint crossing, a route that swaps NotFound in
// and out) renders DockHost straight away instead of suspending again.
const dockHost = createLazyChunkStore(lazyDockHost);

// A payload caches its outcome for the life of the module, success or
// failure, so one test's failed chunk would otherwise be every later test's
// failed chunk. Mirrors the resetXForTests precedent every other module
// singleton here follows (stores/navigation/store.ts's own note); no
// production code should ever call it.
export function resetDockChunkForTests(): void {
  dockHost.publish(lazyDockHost());
}

// The workspace boundary claims every error thrown below it (a module init or
// render bug shows the same failure state as a fetch that never arrived), but
// the reload stays gated on a stale hashed asset - a plain logic failure must
// not prescribe a reload that cannot fix it.
const DockChunkBoundary = createChunkFailureBoundary(() => true, isStaleDockHostChunkError);

export function DockRegion() {
  const generation = conversationHandoffGeneration("desktop");
  const cancelHandoff = useCallback(() => cancelConversationHandoff(generation), [generation]);
  const { Chunk: Host, retry, reloadAvailable } = useChunkRetry(dockHost, lazyDockHost, cancelHandoff);

  return (
    <DockChunkBoundary
      onCatch={cancelHandoff}
      onRetry={retry}
      reloadAvailable={reloadAvailable}
      renderFailure={(state) => <DockFailure {...state} />}
    >
      <Suspense
        fallback={
          <div className={styles.loading} data-testid="dock-loading">
            <Loader label="Loading the workspace…" />
            <Button size="sm" onClick={retry}>
              Retry
            </Button>
          </div>
        }
      >
        <Host />
      </Suspense>
    </DockChunkBoundary>
  );
}
