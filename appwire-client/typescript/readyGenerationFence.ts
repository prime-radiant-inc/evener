// The generation-and-serial bookkeeping a hub-settings store fences every
// await on. A GENERATION is one connected, section-advertising hub's lifetime:
// work captures the generation it started under before it awaits, and its
// reply lands only while that generation is still the active one, the hub
// still advertises the section, and no later work of its own kind has
// superseded it. Retiring a payload supersedes everything in flight without
// ending the generation; ending the generation fences it too.
//
// The store keeps its own state publication (what a retirement clears, what a
// reply applies) and any per-row tokens above this: the fence knows only
// generations, read serials and write tokens.

/** The live fence. Every predicate is safe to call after an await. */
export interface ReadyGenerationFence {
  /** The generation to capture before an await; -1 while none is ready. */
  readonly generation: number;
  /** True once `dispose` has run: the store is finished and nothing lands. */
  readonly disposed: boolean;
  /** The write token in force. A read that captures this before it leaves and
   * finds it unchanged on its reply started after the last write left, so it
   * is authoritative about that write's outcome. */
  readonly writeToken: number;
  /** The generation is still the active one and the store is live. */
  isCurrent(generation: number): boolean;
  /** The hub a piece of work started against is still the one being acted
   * on: its ready generation is current and support is still advertised. */
  liveHub(generation: number): boolean;
  /** A read's reply is its own to land: live hub, and no later read or
   * payload retirement has superseded it. */
  readStillMine(generation: number, serial: number): boolean;
  /** A write's reply is its own to land: live hub, and no later write or
   * payload retirement has superseded it. */
  writeStillMine(generation: number, token: number): boolean;
  /** Claims this read's serial, superseding every earlier read. */
  claimRead(): number;
  /** Claims this write's token, superseding every earlier write. */
  claimWrite(): number;
  /** No authoritative read has confirmed state for the active generation yet.
   * The next payload is authoritative at any revision because a reconnect can
   * be a hub restart with its own revision sequence. */
  readonly awaitingFirstPayload: boolean;
  /** Marks this generation's first authoritative read as applied. */
  firstPayloadApplied(): void;
  /** WHY a write's reply is not its own to land, because the two answers
   * call for opposite things. LOST-HUB: the write's own claim is still
   * intact and only support went away (the unknown window keeps the state
   * and the in-flight work), so nothing else will ever settle this write
   * and the editor must not be left mid-write. SUPERSEDED: a later write, or
   * a payload retirement, has taken over - whoever took over owns `saving`
   * now, and this reply must touch nothing. `stillClaimed` is the caller's
   * own check (keybindingsStore.ts's store-wide write token) - this fence
   * knows only whether the generation itself is still current. */
  lostHub(generation: number, stillClaimed: boolean): boolean;
  /** Supersedes every read and write in flight, leaving the generation
   * active: a payload retirement calls this before publishing its own state. */
  supersede(): void;
  /** Starts a ready generation and returns it, or -1 for a disposed store. */
  begin(): number;
  /** Ends the ready generation. The epoch bump fences every token captured
   * under it, including one captured between this call and the next begin. */
  end(): void;
  /** Ends the generation for good. Returns false when already disposed. */
  dispose(): boolean;
}

/** `isSupported` reports whether the hub still advertises the store's
 * settings section; it is read live, so a support drop fences in-flight work
 * with no bookkeeping of its own. */
export function createReadyGenerationFence(isSupported: () => boolean): ReadyGenerationFence {
  let epoch = 0;
  let activeEpoch = -1;
  let disposed = false;
  let readSerial = 0;
  let writeSerial = 0;
  let awaitingFirstPayload = true;

  function isCurrent(generation: number): boolean {
    return !disposed && generation >= 0 && activeEpoch === generation;
  }

  function liveHub(generation: number): boolean {
    return isCurrent(generation) && isSupported();
  }

  function end(): void {
    activeEpoch = -1;
    epoch += 1;
  }

  return {
    get generation() {
      return activeEpoch;
    },
    get disposed() {
      return disposed;
    },
    get writeToken() {
      return writeSerial;
    },
    isCurrent,
    liveHub,
    readStillMine: (generation, serial) => liveHub(generation) && serial === readSerial,
    writeStillMine: (generation, token) => liveHub(generation) && token === writeSerial,
    claimRead: () => ++readSerial,
    claimWrite: () => ++writeSerial,
    lostHub: (generation, stillClaimed) => stillClaimed && isCurrent(generation),
    get awaitingFirstPayload() {
      return awaitingFirstPayload;
    },
    firstPayloadApplied() {
      awaitingFirstPayload = false;
    },
    supersede() {
      readSerial += 1;
      writeSerial += 1;
    },
    begin() {
      if (disposed) return -1;
      activeEpoch = ++epoch;
      awaitingFirstPayload = true;
      return activeEpoch;
    },
    end,
    dispose() {
      if (disposed) return false;
      disposed = true;
      end();
      return true;
    },
  };
}
