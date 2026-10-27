// Temporary browser-guard-only collector. Source emitters exist only in runtime-trace.patch.
// This function is serialized into a single reload document, before application scripts.
function bootstrap(fixture) {
  const capacity = 2048;
  const ring = new Array(capacity);
  let sequence = 0;
  const kinds = new Set(['paging', 'restore', 'transition', 'compose', 'child', 'binding', 'owner', 'opening']);
  const strings = new Set(['reason', 'outcome', 'readerId', 'binding', 'committedBinding', 'targetRef', 'entryId', 'path', 'pageKey', 'livePageKey', 'lastPage', 'mode', 'availability']);
  const numbers = new Set(['ownerId', 'committedOwnerId', 'promiseId', 'rows', 'entries', 'offset', 'viewportOffset', 'scrollTop', 'commanded', 'revision', 'snapshotId', 'rowsId']);
  const booleans = new Set(['restoring', 'moving', 'opening', 'rootOpening', 'targetOpening', 'alive', 'readable', 'loading', 'pending', 'capturedLoading', 'capturedPending', 'error', 'permanent', 'hasOlder', 'sameOwner', 'sameBinding', 'snapshotHasTarget', 'rowHasTarget', 'currentRows', 'currentReader', 'layoutCurrent', 'sourcePresent', 'activityReady', 'activityLoading', 'activityError', 'activityUnavailable', 'activityHasMore', 'expanded', 'hasChild']);
  const allowedRef = (ref) => ref === fixture.rootRef || ref === fixture.childRef;
  window.addEventListener('comicguard:trace-config', (event) => {
    event.detail.config = { ...fixture };
  });
  window.addEventListener('comicguard:trace', (event) => {
    const input = event.detail;
    if (!input || !allowedRef(input.ref) || !kinds.has(input.kind)) return;
    const row = { sequence: ++sequence, time: performance.now(), ref: input.ref, kind: input.kind };
    for (const [key, value] of Object.entries(input)) {
      if (strings.has(key) && (value === null || (typeof value === 'string' && value.length <= 2048))) row[key] = value;
      if (numbers.has(key) && typeof value === 'number' && Number.isFinite(value)) row[key] = value;
      if (booleans.has(key) && typeof value === 'boolean') row[key] = value;
    }
    ring[(sequence - 1) % capacity] = row;
  });
  // JSON, not live objects or application/store access. No polling or CDP per event.
  window.__comicRuntimeTrace = () => JSON.stringify({
    version: 1, capacity, sequence, dropped: Math.max(0, sequence - capacity),
    timeOrigin: performance.timeOrigin, fixture: { ...fixture },
    events: Array.from({ length: Math.min(sequence, capacity) }, (_, i) => ring[(Math.max(0, sequence - capacity) + i) % capacity]),
  });
}

export function runtimeTraceSource(fixture) {
  return `(${bootstrap.toString()})(${JSON.stringify(fixture)})`;
}
