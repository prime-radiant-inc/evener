// The connect-provider dialog is a lazy chunk (ConnectProviderDialog plus its
// instance-credential editors), so every surface that can open it needs the
// same failure handling: a rejected import() must not unwind into whatever
// boundary happens to sit above the caller, and a retry must mount a NEW lazy
// payload over a cache-busted URL - React.lazy keeps a rejected payload
// forever, and Chrome keeps a failed module fetch by URL (see
// connectDialogChunk.ts).
//
// Two callers mount this module's lazy chunk (ConnectProviderDialog plus its
// instance-credential editors): the spawn pane's below-the-fold dialog and the
// credentials section's full editor.

import { type JSX, type LazyExoticComponent, lazy, type ReactNode } from "react";
import {
  type ChunkFailureFallbackProps,
  createChunkFailureBoundary,
  createLazyChunkStore,
  useChunkRetry,
} from "../../../../shell/chunkFailureBoundary";
import { reloadPage } from "../../../../shell/pageReload";
import { Button, Dialog } from "../../../../widgets";
import { isStaleConnectDialogChunkError, loadConnectDialog } from "../../../spawn/connectDialogChunk";

type ConnectProviderDialogComponent = (props: { onClose(): void; onConnected(name?: string): void }) => JSX.Element;
export type ConnectProviderDialogChunk = LazyExoticComponent<ConnectProviderDialogComponent>;

function lazyConnectProviderDialog(cacheBust = false): ConnectProviderDialogChunk {
  // ConnectProviderDialog is a named export, so the import() promise is
  // adapted the same way App.tsx's own DevHarnessRoute does for
  // dev/DevHarness.tsx.
  return lazy(() => loadConnectDialog(cacheBust).then((m) => ({ default: m.ConnectProviderDialog })));
}

// One shared payload: every surface that can open the dialog mounts it, and a
// successful retry on one of them must reach the others. A surface that
// captured its own mount-time payload would keep re-rendering the rejected
// chunk it already recovered from.
const connectDialog = createLazyChunkStore(lazyConnectProviderDialog);

// A payload caches its outcome for the life of the module, success or
// failure, so one test's failed chunk would otherwise be every later test's
// failed chunk. Mirrors the resetXForTests precedent every other module
// singleton here follows; no production code should ever call it.
export function resetConnectDialogChunkForTests(): void {
  connectDialog.publish(lazyConnectProviderDialog());
}

// useConnectProviderDialogChunk owns the per-caller retry state while the
// payload itself is shared. The returned version changes whenever a retry
// publishes a new payload, so callers key the boundary on it: a surface still
// showing the old failure remounts into the recovered chunk instead of holding
// a stale failure panel.
export function useConnectProviderDialogChunk(): {
  Dialog: ConnectProviderDialogChunk;
  version: number;
  retry: () => void;
  reloadAvailable: boolean;
} {
  const { Chunk, version, retry, reloadAvailable } = useChunkRetry(connectDialog, lazyConnectProviderDialog);
  return { Dialog: Chunk, version, retry, reloadAvailable };
}

interface ConnectDialogFailureProps extends ChunkFailureFallbackProps {
  onClose: () => void;
}

function ConnectDialogFailure({ failure, retry, reloadAvailable, isStaleFailure, onClose }: ConnectDialogFailureProps) {
  return (
    <Dialog
      open
      onClose={onClose}
      title="Couldn't load the connect dialog"
      footer={
        <>
          <Button variant="quiet" onClick={onClose}>
            Close
          </Button>
          <Button variant="primary" onClick={retry}>
            Retry
          </Button>
          {/* reloadAvailable alone counts retries, so a logic bug that rode in
              on a stale chunk URL would earn a page reload that cannot fix
              it. The failed retry must itself name a stale hashed asset. */}
          {reloadAvailable && isStaleFailure(failure) && (
            <Button variant="quiet" onClick={reloadPage}>
              Reload page
            </Button>
          )}
        </>
      }
    >
      <span data-testid="connect-dialog-load-failure">{failure}</span>
    </Dialog>
  );
}

const Boundary = createChunkFailureBoundary(isStaleConnectDialogChunkError);

interface ConnectProviderDialogBoundaryProps {
  // Swaps in a fresh lazy component to load the chunk again. The boundary
  // clears its own failure state alongside it - both halves are needed, and
  // neither is any use without the other.
  onRetry: () => void;
  reloadAvailable: boolean;
  onClose: () => void;
  children: ReactNode;
}

// Scoped to the dialog: without it the lazy() rethrow would bubble into
// whatever boundary happens to sit above the caller - on desktop the dock's
// workspace-failure state (misleading, the workspace is fine) and on mobile
// StackHost mounts panes with no boundary at all, so the whole app.
export function ConnectProviderDialogBoundary({
  onRetry,
  reloadAvailable,
  onClose,
  children,
}: ConnectProviderDialogBoundaryProps): ReactNode {
  return (
    <Boundary
      onRetry={onRetry}
      reloadAvailable={reloadAvailable}
      renderFailure={(state) => <ConnectDialogFailure {...state} onClose={onClose} />}
    >
      {children}
    </Boundary>
  );
}
