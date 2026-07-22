import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { AttentionEntry } from "./attention";
import { fireOsNotification, playTone } from "./channels";

function entry(overrides: Partial<AttentionEntry> = {}): AttentionEntry {
  return { ref: "local:r1", title: "Fix the parser", level: "needs_you", askPending: false, ...overrides };
}

// --- Notification double ---------------------------------------------------
class FakeNotification {
  static permission: NotificationPermission = "granted";
  static instances: FakeNotification[] = [];
  onclick: (() => void) | null = null;
  constructor(readonly title: string) {
    FakeNotification.instances.push(this);
  }
}

// --- AudioContext double ---------------------------------------------------
function oscStub() {
  return { frequency: { value: 0 }, connect: vi.fn(), start: vi.fn(), stop: vi.fn() };
}
class FakeAudioContext {
  static instances: FakeAudioContext[] = [];
  static throwOnOscillator = false;
  closed = false;
  destination = { id: "dest" };
  osc = oscStub();
  gainNode = { gain: { value: 0 }, connect: vi.fn() };
  constructor() {
    FakeAudioContext.instances.push(this);
  }
  createOscillator() {
    if (FakeAudioContext.throwOnOscillator) throw new Error("no audio");
    return this.osc;
  }
  createGain() {
    return this.gainNode;
  }
  close() {
    this.closed = true;
  }
}

beforeEach(() => {
  FakeNotification.instances = [];
  FakeNotification.permission = "granted";
  FakeAudioContext.instances = [];
  FakeAudioContext.throwOnOscillator = false;
  vi.spyOn(document, "hasFocus").mockReturnValue(false); // unfocused by default
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("fireOsNotification", () => {
  test("granted + unfocused: constructs 'serf · <title>'", () => {
    vi.stubGlobal("Notification", FakeNotification);
    fireOsNotification(entry({ title: "Fix the parser" }));
    expect(FakeNotification.instances.map((n) => n.title)).toEqual(["serf · Fix the parser"]);
  });

  test("falls back to the ref when the title is empty", () => {
    vi.stubGlobal("Notification", FakeNotification);
    fireOsNotification(entry({ title: "" }));
    expect(FakeNotification.instances[0]?.title).toBe("serf · local:r1");
  });

  test("permission not granted: no notification", () => {
    FakeNotification.permission = "denied";
    vi.stubGlobal("Notification", FakeNotification);
    fireOsNotification(entry());
    expect(FakeNotification.instances).toHaveLength(0);
  });

  test("focused document: no notification", () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    vi.stubGlobal("Notification", FakeNotification);
    fireOsNotification(entry());
    expect(FakeNotification.instances).toHaveLength(0);
  });

  test("no Notification API: no throw", () => {
    vi.stubGlobal("Notification", undefined);
    expect(() => fireOsNotification(entry())).not.toThrow();
  });

  test("click navigates to the session pane", () => {
    vi.stubGlobal("Notification", FakeNotification);
    const push = vi.spyOn(window.history, "pushState");
    fireOsNotification(entry({ ref: "local:r1" }));
    FakeNotification.instances[0]?.onclick?.();
    expect(push).toHaveBeenCalledWith({}, "", "/s/local%3Ar1");
  });
});

describe("playTone", () => {
  test("unfocused: 800 Hz oscillator, gain 0.1, stopped + context closed after 120 ms", () => {
    vi.useFakeTimers();
    vi.stubGlobal("AudioContext", FakeAudioContext);
    playTone();
    const ctx = FakeAudioContext.instances[0];
    expect(ctx).toBeDefined();
    expect(ctx?.osc.frequency.value).toBe(800);
    expect(ctx?.gainNode.gain.value).toBe(0.1);
    expect(ctx?.osc.connect).toHaveBeenCalledWith(ctx?.gainNode);
    expect(ctx?.gainNode.connect).toHaveBeenCalledWith(ctx?.destination);
    expect(ctx?.osc.start).toHaveBeenCalledOnce();
    expect(ctx?.osc.stop).not.toHaveBeenCalled();
    vi.advanceTimersByTime(120);
    expect(ctx?.osc.stop).toHaveBeenCalledOnce();
    expect(ctx?.closed).toBe(true);
  });

  test("focused document: no audio context created", () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    vi.stubGlobal("AudioContext", FakeAudioContext);
    playTone();
    expect(FakeAudioContext.instances).toHaveLength(0);
  });

  test("no AudioContext constructor: no throw", () => {
    vi.stubGlobal("AudioContext", undefined);
    vi.stubGlobal("webkitAudioContext", undefined);
    expect(() => playTone()).not.toThrow();
  });

  test("a graph-wiring failure is swallowed", () => {
    FakeAudioContext.throwOnOscillator = true;
    vi.stubGlobal("AudioContext", FakeAudioContext);
    expect(() => playTone()).not.toThrow();
  });
});
