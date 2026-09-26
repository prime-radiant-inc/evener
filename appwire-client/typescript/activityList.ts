import {
  type ActivityDelegateBranch,
  type ActivitySessionNode,
  type ActivityTree,
  activityDelegateBranch,
  activityNodeID,
  parseActivityTree,
} from "./activityData";
import { fenceRootSession, graftContinuationTree } from "./activityMerge";
import type { AppwireClient } from "./client";
import { sessionActionError } from "./errors";
import { isActionUnavailable, isThreadNotFound } from "./sessionErrors";

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

/** Own activity requests and retained results for one hub/session lifetime. */
export class ActivityList {
  private state: ActivityState;
  private listeners = new Set<() => void>();
  private unsubscribe?: () => void;
  private disposed = false;
  private dirty = false;
  private queuedBranches: string[] = [];
  private inFlight?: Promise<void>;
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
      if (
        (n.method === "evener/jobs/treeUpdated" ||
          n.method === "evener/job/started" ||
          n.method === "evener/job/finished" ||
          n.method === "evener/delegate/updated" ||
          n.method === "evener/thread/resync") &&
        n.params.ref === this.ref &&
        n.params.threadId === this.threadId
      )
        void this.refresh();
    });
    void this.refresh();
  }
  refresh = (): Promise<void> => {
    if (this.disposed) return Promise.resolve();
    if (this.inFlight) {
      this.dirty = true;
      return this.inFlight;
    }
    return this.run();
  };
  loadMore = (id: string, continuation: string): Promise<void> => {
    if (
      this.disposed ||
      this.state.unsupported ||
      this.state.ended ||
      !this.branches().some((branch) => branch.id === id && branch.continuation === continuation)
    )
      return Promise.resolve();
    if (this.inFlight) {
      if (!this.queuedBranches.includes(id)) this.queuedBranches.push(id);
      return this.inFlight;
    }
    return this.run({ id, continuation });
  };
  private run(branch?: { id: string; continuation: string }) {
    this.inFlight = this.load(branch).finally(() => {
      this.inFlight = undefined;
    });
    return this.inFlight;
  }
  private async load(branch?: { id: string; continuation: string }) {
    this.publish({
      loading: true,
      error: null,
      ...(branch ? {} : { unsupported: false, ended: false }),
    });
    do {
      this.dirty = false;
      try {
        const result = await this.client.request("evener/jobs/list", {
          ref: this.ref,
          ...(branch ? { continuation: branch.continuation } : {}),
        });
        if (!this.dirty && !this.disposed) {
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
          }
        }
      } catch (error) {
        if (!this.dirty && !this.disposed) {
          if (!branch && isActionUnavailable(error)) this.publish({ unsupported: true });
          else if (!branch && isThreadNotFound(error)) this.publish({ ended: true });
          else
            this.publish({
              error: sessionActionError("Could not load activity", error),
            });
          if (!branch) this.queuedBranches = [];
        }
      }
      // Invalidation always gets a full refresh before any queued page. A
      // page click targets a branch; its token comes from the refreshed tree.
      if (this.dirty && branch && !this.queuedBranches.includes(branch.id)) this.queuedBranches.push(branch.id);
      branch = undefined;
      if (!this.dirty) {
        while (this.queuedBranches.length && !branch) {
          const id = this.queuedBranches.shift();
          const current = this.branches().find((candidate) => candidate.id === id);
          if (current?.continuation) branch = { id: current.id, continuation: current.continuation };
        }
      }
    } while ((this.dirty || branch) && !this.disposed);
    this.publish({ loading: false });
  }
  dispose() {
    this.disposed = true;
    this.unsubscribe?.();
    this.listeners.clear();
  }
}
