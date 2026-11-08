/**
 * Shared pointer-focus tracking, one owner per Document.
 *
 * Every mounted floating-label consumer used to install its own set of
 * document-level capture listeners for pointerdown/pointerup/pointercancel.
 * With N consumers that is 3N live listeners doing identical work. This module
 * keeps one State per Document in a WeakMap and reference-counts it, so the
 * first acquire installs the listeners once and the final release takes them
 * down, clears the state and drops the entry.
 *
 * It has no React dependency and no per-consumer callback fan-out: a handle
 * reads the shared State, and the listeners only mutate it.
 */

export interface PointerActivityHandle {
  /** Whether a pointer is currently pressed on the owner, or was released
   * within the compatibility window a touch browser leaves between pointerup
   * and the mouse/focus/click it sends afterwards. */
  isPointerFocus(windowMs: number): boolean;
  /** Release this handle's reference. Idempotent: a second call does nothing. */
  release(): void;
}

interface PointerActivityState {
  refCount: number;
  /** Pointer ids believed down, with the time each press began. */
  readonly pressed: Map<number, number>;
  lastReleaseAt: number;
  readonly press: (event: PointerEvent) => void;
  readonly releasePointer: (event: PointerEvent) => void;
  readonly releaseWithoutButton: (event: PointerEvent) => void;
  readonly releaseHidden: (event: Event) => void;
}

/** Backstop for a press whose pointerup and pointercancel never arrive: an id
 * stops counting as the pointer's own this long after the press began. The
 * tracker also releases presses when the document is hidden and when a move
 * reports no button down, but a lost tap followed by keyboard focus produces
 * neither event, so this bound keeps one lost release from suppressing those
 * reveals for the life of the Document's owner. It assumes the focus a press
 * takes arrives while or shortly after it is down, not this long into it. */
export const POINTER_PRESS_MAX_MS = 10_000;

const states = new WeakMap<Document, PointerActivityState>();

function createState(): PointerActivityState {
  const pressed = new Map<number, number>();
  const state: PointerActivityState = {
    refCount: 0,
    pressed,
    lastReleaseAt: Number.NEGATIVE_INFINITY,
    press: (event) => {
      const now = Date.now();
      pruneExpired(state, now);
      pressed.set(event.pointerId, now);
    },
    // A release drops only the pointer that lifted. A multi-touch gesture can
    // leave another pointer still pressed, which stays counted as focus.
    releasePointer: (event) => {
      pressed.delete(event.pointerId);
      state.lastReleaseAt = Date.now();
    },
    // A move reporting no button down is the pointer's own evidence that the
    // press ended: its release was lost, but it is no longer held.
    releaseWithoutButton: (event) => {
      if (event.buttons === 0) pressed.delete(event.pointerId);
    },
    // A hidden page cannot keep a pointer down, so every press ended. This
    // also bounds ids on documents that never read isPointerFocus.
    releaseHidden: (event) => {
      if ((event.currentTarget as Document | null)?.visibilityState === "hidden") pressed.clear();
    },
  };
  return state;
}

function pruneExpired(state: PointerActivityState, now: number): void {
  for (const [pointerId, pressedAt] of state.pressed) {
    if (now - pressedAt > POINTER_PRESS_MAX_MS) state.pressed.delete(pointerId);
  }
}

function install(owner: Document, state: PointerActivityState): void {
  owner.addEventListener("pointerdown", state.press, true);
  owner.addEventListener("pointerup", state.releasePointer, true);
  owner.addEventListener("pointercancel", state.releasePointer, true);
  owner.addEventListener("pointermove", state.releaseWithoutButton, true);
  owner.addEventListener("visibilitychange", state.releaseHidden, true);
}

function uninstall(owner: Document, state: PointerActivityState): void {
  owner.removeEventListener("pointerdown", state.press, true);
  owner.removeEventListener("pointerup", state.releasePointer, true);
  owner.removeEventListener("pointercancel", state.releasePointer, true);
  owner.removeEventListener("pointermove", state.releaseWithoutButton, true);
  owner.removeEventListener("visibilitychange", state.releaseHidden, true);
}

export function acquirePointerActivity(owner: Document): PointerActivityHandle {
  let state = states.get(owner);
  if (state === undefined) {
    state = createState();
    install(owner, state);
    states.set(owner, state);
  }
  state.refCount += 1;

  const held = state;
  let released = false;
  return {
    isPointerFocus(windowMs) {
      const now = Date.now();
      pruneExpired(held, now);
      return held.pressed.size > 0 || now - held.lastReleaseAt <= windowMs;
    },
    release() {
      if (released) return;
      released = true;
      held.refCount -= 1;
      if (held.refCount > 0) return;
      uninstall(owner, held);
      held.pressed.clear();
      states.delete(owner);
    },
  };
}
