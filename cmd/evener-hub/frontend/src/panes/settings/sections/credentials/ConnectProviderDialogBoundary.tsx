// The connect-provider dialog is a lazy chunk (ConnectProviderDialog plus its
// instance-credential editors), so every surface that can open it needs the
// same failure handling: a rejected import() must not unwind into whatever
// boundary happens to sit above the caller, and a retry must mount a NEW lazy
// payload over a cache-busted URL - React.lazy keeps a rejected payload
// forever, and Chrome keeps a failed module fetch by URL (see
// connectDialogChunk.ts).
//
// Two callers share this module: the spawn pane's below-the-fold dialog and
// the session chrome's model-switch trigger, whose "Connect another provider"
// entry opens the same dialog (previously with only a Suspense fallback, so a
// rejected chunk took the session UI down with no retry).
import { Component, type JSX, type LazyExoticComponent, lazy, type ReactNode, useCallback, useState } from "react";
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

// Module scope, not per mount: a lazy() component caches its resolved module
// on its own payload, so one shared component means the chunk is fetched once
// per page load and every later open renders the dialog straight away instead
// of suspending again.
let connectProviderDialog = lazyConnectProviderDialog();

// A payload caches its outcome for the life of the module, success or
// failure, so one test's failed chunk would otherwise be every later test's
// failed chunk. Mirrors the resetXForTests precedent every other module
// singleton here follows; no production code should ever call it.
export function resetConnectDialogChunkForTests(): void {
  connectProviderDialog = lazyConnectProviderDialog();
}

// useConnectProviderDialogChunk owns the per-caller half of that shared
// payload: the currently mounted lazy component plus the cache-busted retry
// state. Two callers each get their own retry button and reload strike count
// while sharing the fetched module.
export function useConnectProviderDialogChunk(): {
  Dialog: ConnectProviderDialogChunk;
  retry: () => void;
  reloadAvailable: boolean;
} {
  const [dialog, setDialog] = useState<ConnectProviderDialogChunk>(connectProviderDialog);
  // A retry re-fetches the same hashed filename over a cache-busted URL:
  // enough for a transient failure, useless once a deploy has removed the
  // file. Counting retries lets the boundary offer a page reload on the
  // second strike (the DockRegion chunk-boundary pattern).
  const [retryCount, setRetryCount] = useState(0);
  const retry = useCallback(() => {
    setRetryCount((count) => count + 1);
    const next = lazyConnectProviderDialog(true);
    // Publish the new payload before it resolves so a remount during the
    // retry shares the in-flight request instead of restoring the rejected
    // payload that caused the boundary.
    connectProviderDialog = next;
    setDialog(() => next);
  }, []);
  return { Dialog: dialog, retry, reloadAvailable: retryCount > 0 };
}

interface ConnectProviderDialogBoundaryProps {
  // Swaps in a fresh lazy component to load the chunk again. The boundary
  // clears its own failure state alongside it - both halves are needed, and
  // neither is any use without the other.
  onRetry: () => void;
  // True once a cache-busted retry has already failed: a deploy that replaced
  // the hashed chunk filename 404s forever under a new query param, so the
  // second strike offers a page reload instead of another same-file fetch
  // (the DockRegion chunk-boundary pattern).
  reloadAvailable: boolean;
  onClose: () => void;
  children: ReactNode;
}

interface ConnectProviderDialogBoundaryState {
  // The failed chunk's own message ("Failed to fetch dynamically imported
  // module: ..."), shown verbatim - a stated failure is worth more to
  // whoever hits it than a generic apology.
  failure: string | null;
}

// Scoped to the dialog: without it the lazy() rethrow would bubble into
// whatever boundary happens to sit above the caller - on desktop the dock's
// workspace-failure state (misleading, the workspace is fine) and on mobile
// StackHost mounts panes with no boundary at all, so the whole app.
export class ConnectProviderDialogBoundary extends Component<
  ConnectProviderDialogBoundaryProps,
  ConnectProviderDialogBoundaryState
> {
  state: ConnectProviderDialogBoundaryState = { failure: null };

  static getDerivedStateFromError(error: unknown): ConnectProviderDialogBoundaryState {
    // A logic bug inside the resolved dialog (module init, render) surfaces
    // through this same boundary as a chunk-fetch failure does. Only a stale
    // hashed-asset URL (JS or CSS) is a chunk-load failure worth a retry:
    // anything else keeps unwinding to the next boundary above instead of
    // being misreported - and retried - as a network fetch.
    if (!isStaleConnectDialogChunkError(error)) throw error;
    return { failure: error instanceof Error ? error.message : String(error) };
  }

  private retry = () => {
    this.setState({ failure: null });
    this.props.onRetry();
  };

  render(): ReactNode {
    if (this.state.failure === null) return this.props.children;
    return (
      <Dialog
        open
        onClose={this.props.onClose}
        title="Couldn't load the connect dialog"
        footer={
          <>
            <Button variant="quiet" onClick={this.props.onClose}>
              Close
            </Button>
            <Button variant="primary" onClick={this.retry}>
              Retry
            </Button>
            {/* reloadAvailable alone counts retries, so a logic bug that rode
                in on a stale chunk URL would earn a page reload that cannot
                fix it. The failed retry must itself name a stale hashed
                asset (the DockRegion chunk-boundary pattern). */}
            {this.props.reloadAvailable && isStaleConnectDialogChunkError(this.state.failure) && (
              <Button variant="quiet" onClick={() => window.location.reload()}>
                Reload page
              </Button>
            )}
          </>
        }
      >
        {this.state.failure}
      </Dialog>
    );
  }
}
