import {
  type ActivityDelegateBranch,
  type ActivitySessionNode,
  type ActivityTree,
  activityDelegateBranch,
  activityNodeID,
  parseActivityTree,
} from "./activityData";
import { applyDelegateUpdate, fenceRootSession, graftContinuationTree } from "./activityMerge";
import type { AppwireClient } from "./client";
import { sessionActionError } from "./errors";
import { isActionUnavailable, isThreadNotFound } from "./sessionErrors";
import type { AnyNotification } from "./types.gen";

// Least time between two whole-tree fetches. Answering evener/jobs/list for a
// session with hundreds of delegates takes seconds and megabytes, while the
// notifications that invalidate it can arrive every second, so a refetch per
// notification never catches up and starves every other request on the socket.
// A refresh asked for inside this window runs once, when it closes.
export const ACTIVITY_REFRESH_MIN_INTERVAL_MS = 2000;

export type ActivityClient = Pick<AppwireClient, "request" | "onNotification">;

export interface ActivityState {
  tree: ActivityTree | null;
  error: string | null;
  loading: boolean;
  unsupported: boolean;
  ended: boolean;
}
export interface ActivityBranch extends ActivityDelegateBranch {
  id: string;
  label: string;
  // Only the root's branch carries diagnostics: the root has no row, so what
  // the daemon could not read of its journals is the footer's to say. A
  // delegate's own and its child's are the row's (activityDelegateDiagnostics).
  diagnostics?: string[];
}

const delay = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

/** Own activity requests and retained results for one hub/session lifetime. */
export class ActivityList {
  private state: ActivityState;
  private listeners = new Set<() => void>();
  private unsubscribe?: () => void;
  private disposed = false;
  private dirty = false;
  private queuedBranches: string[] = [];
  private inFlight?: Promise<void>;
  private lastRootLoadEnd?: number;
  // Whole-tree fetches started so far, and how many had started when the
  // latest page was queued.
  private rootLoads = 0;
  private queuedAfterRoot = 0;
  private trailing = false;
  constructor(
    private client: ActivityClient,
    private ref: string,
    private threadId: string,
    tree: ActivityTree | null = null,
  ) {
    if (!ref.trim() || !threadId.trim()) throw new TypeError("Activity requires a session ref and thread ID");
    if (tree && (tree.root.ref !== ref || tree.root.sessionId !== threadId))
      throw new TypeError("Retained activity belongs to another session");
    this.state = {
      tree,
      error: null,
      loading: false,
      unsupported: false,
      ended: false,
    };
  }
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<ActivityState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener();
  }
  branches = (): ActivityBranch[] => {
    const branches: ActivityBranch[] = [];
    const append = (id: string, label: string, branch: ActivityDelegateBranch) => {
      if (branch.error || branch.truncated || branch.continuation) branches.push({ id, label, ...branch });
    };
    // One owner per token. A delegate row answers for the child session it
    // rendered -- activityDelegateBranch reads that child's branch as well as
    // the delegate's own -- so appending the child as a row of its own would
    // repeat the same continuation under a second id: a duplicate "Load more"
    // whose session-keyed id no delegate graft matches, or a second,
    // un-actionable copy of a row the delegate already reports. Only the root
    // speaks for itself; every other session is reached through a delegate.
    const visitDelegates = (node: ActivitySessionNode) => {
      for (const entry of node.entries)
        if (entry.kind === "delegate") {
          append(
            activityNodeID(entry),
            entry.delegate.description ?? entry.delegate.childRef,
            activityDelegateBranch(entry.delegate),
          );
          if (entry.delegate.child) visitDelegates(entry.delegate.child);
        }
    };
    const root = this.state.tree?.root;
    if (root) {
      if (root.diagnostics?.length)
        branches.push({ id: activityNodeID(root), label: root.label, ...root.branch, diagnostics: root.diagnostics });
      else append(activityNodeID(root), root.label, root.branch);
      visitDelegates(root);
    }
    return branches;
  };
  start() {
    if (this.disposed || this.unsubscribe) return;
    this.unsubscribe = this.client.onNotification((n) => {
      if (this.applyNotification(n)) this.requestRefresh();
    });
    void this.refresh();
  }
  /**
   * Folds a notification into the held tree where it can, and returns whether
   * the whole tree has to be fetched again to show what it announces. For a
   * caller that runs its own reloads instead of start().
   */
  applyNotification(n: AnyNotification): boolean {
    switch (n.method) {
      case "evener/delegate/updated": {
        if (!this.owns(n.params)) return false;
        const tree = this.state.tree;
        const updated = tree && applyDelegateUpdate(tree, n.params.delegate);
        if (!updated) return true;
        if (updated !== tree) this.publish({ tree: updated });
        return false;
      }
      case "evener/jobs/treeUpdated":
        // A tree at or past this revision already shows what it announces.
        return this.owns(n.params) && (!this.state.tree || n.params.revision > this.state.tree.revision);
      case "evener/job/started":
      case "evener/job/finished":
      case "evener/thread/resync":
        return this.owns(n.params);
      default:
        return false;
    }
  }
  /** Resolves once the minimum interval since the last whole-tree fetch has
   * passed, rechecking after each wait since a fetch may have ended meanwhile. */
  async waitForRefreshWindow(): Promise<void> {
    for (let wait = this.untilRootLoadAllowed(); wait > 0; wait = this.untilRootLoadAllowed()) await delay(wait);
  }
  private owns(params: { ref: string; threadId: string }) {
    return params.ref === this.ref && params.threadId === this.threadId;
  }
  refresh = (): Promise<void> => {
    if (this.disposed) return Promise.resolve();
    if (this.inFlight) {
      this.dirty = true;
      return this.inFlight;
    }
    return this.run();
  };
  // The refresh a notification asks for. Inside the minimum interval it is
  // deferred to the interval's end, and any number of requests share that one.
  private requestRefresh() {
    const wait = this.inFlight ? 0 : this.untilRootLoadAllowed();
    if (wait <= 0) {
      void this.refresh();
      return;
    }
    if (this.trailing) return;
    this.trailing = true;
    void delay(wait).then(() => {
      this.trailing = false;
      // A fetch made while this waited may have moved the window.
      this.requestRefresh();
    });
  }
  // Milliseconds until a whole-tree fetch may start again.
  private untilRootLoadAllowed() {
    if (this.lastRootLoadEnd === undefined) return 0;
    return Math.max(0, this.lastRootLoadEnd + ACTIVITY_REFRESH_MIN_INTERVAL_MS - Date.now());
  }
  loadMore = (id: string, continuation: string): Promise<void> => {
    if (
      this.disposed ||
      this.state.unsupported ||
      this.state.ended ||
      !this.branches().some((branch) => branch.id === id && branch.continuation === continuation)
    )
      return Promise.resolve();
    if (this.inFlight) {
      this.queuePage(id);
      return this.inFlight;
    }
    return this.run({ id, continuation });
  };
  // A page waits for a root fetch that starts after this call: its token comes
  // from that tree, not from one a notification may already have outdated.
  private queuePage(id: string) {
    if (!this.queuedBranches.includes(id)) this.queuedBranches.push(id);
    this.queuedAfterRoot = this.rootLoads;
  }
  private run(branch?: { id: string; continuation: string }) {
    const running = this.load(branch).finally(() => {
      if (this.inFlight === running) this.inFlight = undefined;
    });
    this.inFlight = running;
    return running;
  }
  private async load(branch?: { id: string; continuation: string }) {
    this.publish({
      loading: true,
      error: null,
      ...(branch ? {} : { unsupported: false, ended: false }),
    });
    // A root fetch is owed after the pages that ran ahead of it.
    let rootOwed = false;
    do {
      this.dirty = false;
      if (!branch) this.rootLoads++;
      const rootLoad = this.rootLoads;
      // The page this iteration asked for was thrown away and has to be asked
      // for again against a fresh root; accepted means its answer is on screen.
      let retry = false;
      let accepted = false;
      try {
        const result = await this.client.request("evener/jobs/list", {
          ref: this.ref,
          ...(branch ? { continuation: branch.continuation } : {}),
        });
        // An answer is shown even when a notification has arrived since the
        // request, which keeps a burst of notifications from starving every
        // load of its result. A continuation page is checked against the
        // revision of the tree it is grafted onto instead.
        if (!this.disposed) {
          const tree = parseActivityTree((result as { data: unknown }).data);
          if (!tree) throw new Error("Invalid activity response");
          if (tree.root.sessionId !== this.threadId || tree.root.ref !== this.ref)
            throw new Error("Activity belongs to another session");
          const current = this.state.tree;
          if (current && branch && tree.revision !== current.revision) {
            // A continuation page is minted against one revision. A page from a
            // different revision names positions that no longer line up with the
            // retained tree, so it is discarded rather than grafted: mark the
            // load dirty and loop, which issues a fresh root request at the
            // current revision. The discarded branch is queued (below) and, if
            // the fresh root still carries its continuation, re-issued against
            // the new revision's token.
            this.dirty = true;
            retry = true;
          } else {
            if (current && tree.revision < current.revision)
              throw new Error("Activity response is older than the displayed activity");
            this.publish({
              // Queued pages run back to back inside one load, which clears the
              // error only once on entry; a page that succeeds after an earlier
              // one failed has to clear it itself.
              error: null,
              tree: current
                ? branch
                  ? graftContinuationTree(current, branch.id, tree)
                  : { ...tree, root: fenceRootSession(current.root, tree.root) }
                : tree,
            });
            accepted = true;
          }
        }
      } catch (error) {
        if (this.dirty) retry = true;
        else if (!this.disposed) {
          if (!branch && isActionUnavailable(error)) this.publish({ unsupported: true });
          else if (!branch && isThreadNotFound(error)) this.publish({ ended: true });
          else
            this.publish({
              error: sessionActionError("Could not load activity", error),
            });
          if (!branch) this.queuedBranches = [];
        }
      }
      if (!branch) this.lastRootLoadEnd = Date.now();
      if (rootOwed) {
        this.dirty = true;
        rootOwed = false;
      }
      if (retry && branch && !this.queuedBranches.includes(branch.id)) this.queuePage(branch.id);
      const wasPage = branch !== undefined;
      branch = undefined;
      // Pages queued behind a load drain before the next whole-tree fetch. The
      // one exception is a page whose tree is out of date: an invalidation
      // before the page was asked for gets a full refresh first, since a page
      // click targets a branch and its token comes from the refreshed tree. A
      // root fetch that started after the page was queued is that refresh, or
      // a page that just landed left the tree current, even if a notification
      // has arrived since; the notification's own root fetch follows the pages.
      const treeCurrent = accepted && (wasPage || rootLoad > this.queuedAfterRoot);
      if (!this.dirty || treeCurrent) {
        while (this.queuedBranches.length && !branch) {
          const id = this.queuedBranches.shift();
          const current = this.branches().find((candidate) => candidate.id === id);
          if (current?.continuation) branch = { id: current.id, continuation: current.continuation };
        }
        if (branch && this.dirty) rootOwed = true;
      }
      // The whole-tree fetch an invalidation asks for waits out the minimum
      // interval since the last one; a queued page is not held to it.
      if (this.dirty && !branch && !this.disposed) {
        const wait = this.untilRootLoadAllowed();
        if (wait > 0) await delay(wait);
      }
    } while ((this.dirty || branch) && !this.disposed);
    // Clear inFlight before the completion publish. A listener that reacts to
    // `loading: false` by requesting the next page (or a refresh) must not find
    // this load still marked in flight: the drain above has already run, so its
    // request would be queued onto an empty loop and dropped until some later
    // load. Clearing first lets it start its own run.
    this.inFlight = undefined;
    this.publish({ loading: false });
  }
  dispose() {
    this.disposed = true;
    this.unsubscribe?.();
    this.listeners.clear();
  }
}
