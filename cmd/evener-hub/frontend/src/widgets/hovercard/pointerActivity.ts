/**
 * Shared pointer-focus tracking, one owner per Document.
 *
 * Every mounted floating-label consumer used to install its own set of
 * document-level capture listeners for pointerdown/pointerup/pointercancel.
 * With N consumers that is 3N live listeners doing identical work. This module
 * keeps one State per Document in a WeakMap and reference-counts it, so the
 * first acquire installs the three listeners once and the final release takes
 * them down, clears the state and drops the entry.
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
  readonly pressed: Set<number>;
  lastReleaseAt: number;
  readonly press: (event: PointerEvent) => void;
  readonly releasePointer: (event: PointerEvent) => void;
}

const states = new WeakMap<Document, PointerActivityState>();

function createState(): PointerActivityState {
  const pressed = new Set<number>();
  const state: PointerActivityState = {
    refCount: 0,
    pressed,
    lastReleaseAt: Number.NEGATIVE_INFINITY,
    press: (event) => {
      pressed.add(event.pointerId);
    },
    // A release drops only the pointer that lifted. A multi-touch gesture can
    // leave another pointer still pressed, which stays counted as focus.
    releasePointer: (event) => {
      pressed.delete(event.pointerId);
      state.lastReleaseAt = Date.now();
    },
  };
  return state;
}

function install(owner: Document, state: PointerActivityState): void {
  owner.addEventListener("pointerdown", state.press, true);
  owner.addEventListener("pointerup", state.releasePointer, true);
  owner.addEventListener("pointercancel", state.releasePointer, true);
}

function uninstall(owner: Document, state: PointerActivityState): void {
  owner.removeEventListener("pointerdown", state.press, true);
  owner.removeEventListener("pointerup", state.releasePointer, true);
  owner.removeEventListener("pointercancel", state.releasePointer, true);
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
      return held.pressed.size > 0 || Date.now() - held.lastReleaseAt <= windowMs;
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
