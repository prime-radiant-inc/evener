import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { type ChangeEvent, createElement, useState } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { SettingsDraftsProvider, useSettingsDirty } from "../panes/settings/settingsDrafts";
import type { AttentionEntry } from "./attention";
import { fireOsNotification, playTone } from "./channels";

function entry(overrides: Partial<AttentionEntry> = {}): AttentionEntry {
  return {
    ref: "local:r1",
    title: "Fix the parser",
    level: "needs_you",
    askPending: false,
    approvalPending: false,
    needsResponse: false,
    ...overrides,
  };
}

function SettingsEditor() {
  const [value, setValue] = useState("original");
  useSettingsDirty(value !== "original");
  return createElement("input", {
    "aria-label": "Settings draft",
    value,
    onChange: (event: ChangeEvent<HTMLInputElement>) => setValue(event.target.value),
  });
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
  window.history.replaceState({}, "", "/");
  FakeNotification.instances = [];
  FakeNotification.permission = "granted";
  FakeAudioContext.instances = [];
  FakeAudioContext.throwOnOscillator = false;
  vi.spyOn(document, "hasFocus").mockReturnValue(false); // unfocused by default
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("fireOsNotification", () => {
  test("granted + unfocused: constructs 'evener · <title>'", () => {
    vi.stubGlobal("Notification", FakeNotification);
    fireOsNotification(entry({ title: "Fix the parser" }));
    expect(FakeNotification.instances.map((n) => n.title)).toEqual(["evener · Fix the parser"]);
  });

  test("falls back to the ref when the title is empty", () => {
    vi.stubGlobal("Notification", FakeNotification);
    fireOsNotification(entry({ title: "" }));
    expect(FakeNotification.instances[0]?.title).toBe("evener · local:r1");
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

  test("click navigates to the session pane", async () => {
    window.history.replaceState({ notificationFixture: true }, "", "/settings/theme");
    vi.stubGlobal("Notification", FakeNotification);
    const focus = vi.spyOn(window, "focus").mockImplementation(() => {});
    const push = vi.spyOn(window.history, "pushState");
    const replace = vi.spyOn(window.history, "replaceState");
    fireOsNotification(entry({ ref: "local:r1" }));
    FakeNotification.instances[0]?.onclick?.();
    expect(focus).toHaveBeenCalledOnce();
    expect(replace).toHaveBeenCalledExactlyOnceWith({ notificationFixture: true, evenerNavigationIndex: 0 }, "");
    expect(push).toHaveBeenCalledExactlyOnceWith({ evenerNavigationIndex: 1 }, "", "/s/local%3Ar1");
    expect(window.location.pathname).toBe("/s/local%3Ar1");
    expect(window.history.state).toEqual({ evenerNavigationIndex: 1 });
    window.history.back();
    await vi.waitFor(() => expect(window.location.pathname).toBe("/settings/theme"));
    expect(window.history.state).toEqual({ notificationFixture: true, evenerNavigationIndex: 0 });
    window.history.forward();
    await vi.waitFor(() => expect(window.location.pathname).toBe("/s/local%3Ar1"));
    expect(window.history.state).toEqual({ evenerNavigationIndex: 1 });
  });

  test("click asks before leaving dirty Settings, Stay retains input, and Discard visits the first notification once", () => {
    window.history.replaceState({ evenerNavigationIndex: 37 }, "", "/settings/agents-md");
    vi.stubGlobal("Notification", FakeNotification);
    const focus = vi.spyOn(window, "focus").mockImplementation(() => {});
    render(createElement(SettingsDraftsProvider, null, createElement(SettingsEditor)));
    const editor = screen.getByRole("textbox", { name: "Settings draft" });
    fireEvent.change(editor, { target: { value: "unsaved notification draft" } });
    const push = vi.spyOn(window.history, "pushState");
    const replace = vi.spyOn(window.history, "replaceState");
    fireOsNotification(entry({ ref: "local:r1" }));
    act(() => FakeNotification.instances[0]?.onclick?.());
    expect(focus).toHaveBeenCalledOnce();
    expect(window.location.pathname).toBe("/settings/agents-md");
    expect(window.history.state).toEqual({ evenerNavigationIndex: 37 });
    expect(push).not.toHaveBeenCalled();
    expect(replace).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Stay" }));
    expect(screen.getByRole("textbox", { name: "Settings draft" })).toBe(editor);
    expect(editor).toHaveProperty("value", "unsaved notification draft");
    expect(screen.queryByRole("button", { name: "Discard changes" })).toBeNull();
    expect(window.location.pathname).toBe("/settings/agents-md");
    expect(window.history.state).toEqual({ evenerNavigationIndex: 37 });
    expect(push).not.toHaveBeenCalled();
    act(() => FakeNotification.instances[0]?.onclick?.());
    fireOsNotification(entry({ ref: "local:r2" }));
    act(() => FakeNotification.instances[1]?.onclick?.());
    expect(push).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Discard changes" }));
    expect(focus).toHaveBeenCalledTimes(3);
    expect(push).toHaveBeenCalledExactlyOnceWith({ evenerNavigationIndex: 38 }, "", "/s/local%3Ar1");
    expect(replace).not.toHaveBeenCalled();
    expect(window.location.pathname).toBe("/s/local%3Ar1");
    expect(window.history.state).toEqual({ evenerNavigationIndex: 38 });
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
