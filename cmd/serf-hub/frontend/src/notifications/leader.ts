// Single-tab leader election, Web Locks ONLY (floor §3.7, hazard #2 — there
// is deliberately NO BroadcastChannel: cross-tab state sync is a landed
// non-goal, prefs.ts:57-62). The first tab to acquire the named lock holds
// it for its whole lifetime (its callback returns a promise that never
// resolves), so every later tab's ifAvailable request is handed null and
// self-identifies as a follower. An environment with no Web Locks API — or a
// request that throws or rejects — falls back to treating EVERY tab as
// leader: a duplicate alert is a smaller problem than a silent one.
const LOCK_NAME = "serf-hub-os-leader";

let leader = false;

// Whether this tab may fire OS/sound alerts. Read at fire time (not election
// time), so the async election settling before the first real transition is
// all the ordering this needs.
export function isLeader(): boolean {
  return leader;
}

export function electLeader(): void {
  const locks = navigator.locks;
  if (!locks?.request) {
    leader = true;
    return;
  }
  try {
    locks
      .request(LOCK_NAME, { ifAvailable: true }, (lock) => {
        if (lock) {
          leader = true;
          // Hold the lock for this tab's lifetime: never resolves.
          return new Promise<void>(() => undefined);
        }
        leader = false;
        return Promise.resolve();
      })
      .catch(() => {
        leader = true;
      });
  } catch {
    leader = true;
  }
}

// Test-only: force the elected state (mirrors the legacy's setLeaderForTest,
// so an edge-fire test can exercise the leader/follower gate without a real
// Web Locks round trip). No production code should call these.
export function setLeaderForTests(value: boolean): void {
  leader = value;
}

export function resetLeaderForTests(): void {
  leader = false;
}
