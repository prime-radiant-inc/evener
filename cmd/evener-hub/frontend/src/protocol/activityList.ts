import {
  type ActivityBranchState,
  type ActivitySessionNode,
  type ActivityTree,
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
export interface ActivityBranch extends ActivityBranchState {
  id: string;
  label: string;
}

/** Own activity requests and retained results for one hub/session lifetime. */
export class ActivityList {
  private state: ActivityState;
  private listeners = new Set<() => void>();
  private unsubscribe?: () => void;
  private disposed = false;
  private dirty = false;
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
    const append = (id: string, label: string, branch: ActivityBranchState) => {
      if (branch.error || branch.truncated || branch.continuation) branches.push({ id, label, ...branch });
    };
    const visit = (node: ActivitySessionNode) => {
      append(activityNodeID(node), node.label, node.branch);
      for (const entry of node.entries)
        if (entry.kind === "delegate") {
          append(activityNodeID(entry), entry.delegate.description ?? entry.delegate.childRef, entry.delegate.branch);
          if (entry.delegate.child) visit(entry.delegate.child);
        }
    };
    if (this.state.tree) visit(this.state.tree.root);
    return branches;
  };
  start() {
    if (this.disposed || this.unsubscribe) return;
    this.unsubscribe = this.client.onNotification((n) => {
      if (
        (n.method === "evener/jobs/treeUpdated" ||
          n.method === "evener/job/started" ||
          n.method === "evener/job/finished" ||
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
      this.inFlight ||
      !this.branches().some((branch) => branch.id === id && branch.continuation === continuation)
    )
      return Promise.resolve();
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
      unsupported: false,
      ended: false,
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
          if (current && tree.revision < current.revision)
            throw new Error("Activity response is older than the displayed activity");
          this.publish({
            tree: current
              ? branch
                ? graftContinuationTree(current, tree, branch.id)
                : { ...tree, root: fenceRootSession(current.root, tree.root) }
              : tree,
          });
        }
      } catch (error) {
        if (!this.dirty && !this.disposed) {
          if (!branch && isActionUnavailable(error)) this.publish({ unsupported: true });
          else if (!branch && isThreadNotFound(error)) this.publish({ ended: true });
          else
            this.publish({
              error: sessionActionError("Could not load activity", error),
            });
        }
      }
      branch = undefined;
    } while (this.dirty && !this.disposed);
    this.publish({ loading: false });
  }
  dispose() {
    this.disposed = true;
    this.unsubscribe?.();
    this.listeners.clear();
  }
}
