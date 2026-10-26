// The composer's verbs and the wire methods that carry them, stated once. Both
// apps' composers, the pending-turns reconciliation, the queued-message gates
// and the native direct fallback all read this map, so a verb added or renamed
// here cannot leave one of them behind. A test holds every value against the
// generated method names.

export const COMPOSER_METHODS = {
  send: "turn/start",
  steer: "turn/steer",
  queue: "turn/queue",
  drain: "turn/drainAsSteer",
  promote: "turn/promoteQueuedAsSteer",
} as const;

export type ComposerVerb = keyof typeof COMPOSER_METHODS;

// The wire methods that name pending composer input - the family whose rows the
// daemon's pending projection lists, and therefore the only family whose absence
// from an authoritative projection proves delivery.
export const PENDING_TURN_METHODS: readonly string[] = Object.freeze(Object.values(COMPOSER_METHODS));

// The composer's four send verbs: the only mutations that go out as a plain
// request when storage refuses their durable write. Stop and promote keep
// their durable contract and fail instead.
export const DIRECT_FALLBACK_METHODS: ReadonlySet<string> = new Set([
  COMPOSER_METHODS.send,
  COMPOSER_METHODS.queue,
  COMPOSER_METHODS.steer,
  COMPOSER_METHODS.drain,
]);

const VERB_BY_METHOD: ReadonlyMap<string, ComposerVerb> = new Map(
  (Object.keys(COMPOSER_METHODS) as ComposerVerb[]).map((verb) => [COMPOSER_METHODS[verb], verb]),
);

// The verb a wire method carries, or undefined for any other method.
export function composerVerb(method: string): ComposerVerb | undefined {
  return VERB_BY_METHOD.get(method);
}
