import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { canonicalFixture } from "../core/fixtures";
import {
  createNavigationController,
  type NavigationController,
} from "../core/history";
import { getPlatformPrimitives } from "../core/platform";
import { createPrototypeStore, PrototypeProvider } from "../core/store";

interface SavedDescriptor {
  descriptor: PropertyDescriptor | undefined;
  key: PropertyKey;
  target: object;
}

const attempts: string[] = [];
const savedDescriptors: SavedDescriptor[] = [];
const controllers: NavigationController[] = [];

function throwingSentinel(name: string): (...args: unknown[]) => never {
  return function sentinel() {
    attempts.push(name);
    throw new Error(`forbidden runtime capability attempted: ${name}`);
  };
}

function replaceDescriptor(
  target: object,
  key: PropertyKey,
  descriptor: PropertyDescriptor,
): void {
  savedDescriptors.push({
    target,
    key,
    descriptor: Object.getOwnPropertyDescriptor(target, key),
  });
  Object.defineProperty(target, key, {
    configurable: true,
    ...descriptor,
  });
}

function replaceValue(target: object, key: PropertyKey, value: unknown): void {
  replaceDescriptor(target, key, { value, writable: true });
}

function capabilityObject(entries: readonly string[]): object {
  const object: Record<string, unknown> = {};
  for (const entry of entries) object[entry] = throwingSentinel(entry);
  return object;
}

function capabilityConstructor(
  name: string,
  methods: readonly string[] = [],
): (...args: unknown[]) => never {
  const trappedConstructor = throwingSentinel(name);
  for (const method of methods) {
    Object.defineProperty(trappedConstructor.prototype, method, {
      configurable: true,
      value: throwingSentinel(`${name}.${method}`),
      writable: true,
    });
  }
  return trappedConstructor;
}

function installCapabilityTrap(): void {
  attempts.length = 0;
  for (const name of [
    "fetch",
    "XMLHttpRequest",
    "WebSocket",
    "EventSource",
    "SpeechRecognition",
    "webkitSpeechRecognition",
    "SpeechSynthesisUtterance",
    "AudioContext",
    "webkitAudioContext",
    "showOpenFilePicker",
    "showSaveFilePicker",
    "showDirectoryPicker",
  ]) {
    replaceValue(window, name, capabilityConstructor(name));
  }

  replaceValue(
    navigator,
    "sendBeacon",
    throwingSentinel("navigator.sendBeacon"),
  );
  replaceValue(
    navigator,
    "mediaDevices",
    capabilityObject(["navigator.mediaDevices.getUserMedia"]),
  );
  replaceValue(
    window,
    "speechSynthesis",
    capabilityObject([
      "speechSynthesis.speak",
      "speechSynthesis.cancel",
      "speechSynthesis.pause",
      "speechSynthesis.resume",
      "speechSynthesis.getVoices",
    ]),
  );
  replaceValue(
    navigator,
    "geolocation",
    capabilityObject([
      "geolocation.getCurrentPosition",
      "geolocation.watchPosition",
      "geolocation.clearWatch",
    ]),
  );
  replaceValue(navigator, "vibrate", throwingSentinel("navigator.vibrate"));

  const notification = capabilityConstructor("Notification");
  Object.defineProperty(notification, "requestPermission", {
    configurable: true,
    value: throwingSentinel("Notification.requestPermission"),
    writable: true,
  });
  replaceValue(window, "Notification", notification);

  const serviceWorker = capabilityObject([
    "serviceWorker.register",
    "serviceWorker.getRegistration",
    "serviceWorker.getRegistrations",
  ]);
  Object.defineProperty(serviceWorker, "ready", {
    configurable: true,
    get: throwingSentinel("serviceWorker.ready"),
  });
  replaceValue(navigator, "serviceWorker", serviceWorker);
  replaceValue(
    window,
    "ServiceWorkerRegistration",
    capabilityConstructor("ServiceWorkerRegistration", [
      "showNotification",
      "getNotifications",
    ]),
  );
  replaceValue(
    window,
    "PushManager",
    capabilityConstructor("PushManager", [
      "subscribe",
      "getSubscription",
      "permissionState",
    ]),
  );
  replaceValue(
    window,
    "PushSubscription",
    capabilityConstructor("PushSubscription", ["unsubscribe"]),
  );
}

function restoreCapabilityDescriptors(): void {
  for (const saved of savedDescriptors.splice(0).reverse()) {
    if (saved.descriptor) {
      Object.defineProperty(saved.target, saved.key, saved.descriptor);
    } else {
      Reflect.deleteProperty(saved.target, saved.key);
    }
  }
}

function nextPopState(): Promise<PopStateEvent> {
  return new Promise((resolve, reject) => {
    const timeout = window.setTimeout(() => {
      window.removeEventListener("popstate", onPopState);
      reject(new Error("timed out awaiting capability-smoke popstate"));
    }, 1_000);
    const onPopState = (event: PopStateEvent) => {
      window.clearTimeout(timeout);
      window.removeEventListener("popstate", onPopState);
      resolve(event);
    };
    window.addEventListener("popstate", onPopState);
  });
}

function main(route: string): HTMLElement {
  const current = screen.getByRole("main");
  expect(current).toHaveAttribute("data-route", route);
  return current;
}

function domainControl(scope: ParentNode, selector: string): HTMLElement {
  const element = scope.querySelector(selector);
  expect(element).toBeInstanceOf(HTMLElement);
  if (!(element instanceof HTMLElement)) throw new Error(`missing ${selector}`);
  if (element.matches("button, a[href]")) return element;
  const control = element.querySelector("button, a[href]");
  expect(control).toBeInstanceOf(HTMLElement);
  return control as HTMLElement;
}

async function back(): Promise<void> {
  const popped = nextPopState();
  fireEvent.click(screen.getByRole("button", { name: "Back" }));
  await popped;
}

beforeEach(() => window.history.replaceState(null, "", "/"));
afterEach(() => {
  cleanup();
  for (const controller of controllers.splice(0)) controller.dispose();
  restoreCapabilityDescriptors();
  vi.restoreAllMocks();
});

describe("runtime capability isolation", () => {
  it("installs every trap before importing RootApp and completes the offline interaction smoke", async () => {
    installCapabilityTrap();
    const [{ RootApp }, { LabControls }] = await Promise.all([
      import("../app/RootApp"),
      import("../app/LabControls"),
    ]);

    const values = new Map<string, string>();
    const store = createPrototypeStore({
      platform: "ios",
      storage: {
        getItem: (key) => values.get(key) ?? null,
        setItem: (key, value) => values.set(key, value),
        removeItem: (key) => values.delete(key),
      },
      fixtureInput: canonicalFixture,
      diagnostics: { report: vi.fn() },
    });
    const controller = createNavigationController(store, window);
    controllers.push(controller);
    const primitives = getPlatformPrimitives("ios");
    render(
      <PrototypeProvider store={store}>
        <RootApp dispatch={controller.dispatch} primitives={primitives} />
        <LabControls dispatch={controller.dispatch} primitives={primitives} />
      </PrototypeProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Select Stillwater" }));
    const sessions = main("sessions");
    fireEvent.change(
      screen.getByRole("searchbox", { name: "Filter sessions" }),
      {
        target: { value: "Native mobile client" },
      },
    );
    fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
    fireEvent.click(
      sessions.querySelector('[data-action="complete-refresh"]') as HTMLElement,
    );
    fireEvent.click(
      domainControl(sessions, '[data-session-id="session-native-client"]'),
    );
    await waitFor(() => expect(main("conversation")).toBeVisible());

    fireEvent.click(
      within(main("conversation")).getByRole("button", {
        name: "Inspect fixture schema",
      }),
    );
    fireEvent.change(screen.getByRole("textbox", { name: "Message" }), {
      target: { value: "Offline smoke" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Submit message" }));
    fireEvent.click(screen.getByRole("button", { name: "Complete response" }));
    fireEvent.click(screen.getByRole("button", { name: "Work" }));
    await waitFor(() => expect(main("work")).toBeVisible());
    fireEvent.click(
      domainControl(main("work"), '[data-work-node-id="work-task-shell"]'),
    );
    await back();
    await waitFor(() => expect(main("conversation")).toBeVisible());

    fireEvent.click(screen.getByRole("button", { name: "Voice" }));
    await waitFor(() => expect(main("voice")).toBeVisible());
    fireEvent.click(
      screen.getByRole("button", { name: "Set voice state: speaking" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    expect(store.getState().voice).toMatchObject({
      stopped: true,
      ended: false,
    });
    fireEvent.click(screen.getByRole("button", { name: "End" }));
    await waitFor(() => expect(main("conversation")).toBeVisible());
    await back();
    expect(store.getState().route.kind).toBe("conversation");
    await back();
    await waitFor(() => expect(main("sessions")).toBeVisible());

    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    fireEvent.change(screen.getByRole("searchbox", { name: "Search" }), {
      target: { value: "Fixture-first plan" },
    });
    fireEvent.click(
      domainControl(
        main("search"),
        '[data-search-result-id="search-transcript"]',
      ),
    );
    await waitFor(() =>
      expect(
        document.querySelector(
          '[data-transcript-item-id="item-assistant-plan"]',
        ),
      ).toHaveFocus(),
    );
    await back();
    await waitFor(() => expect(main("search")).toBeVisible());

    fireEvent.click(screen.getByRole("button", { name: "New Session" }));
    const project = canonicalFixture.recentProjects[0];
    expect(project).toBeDefined();
    if (!project) return;
    fireEvent.click(
      domainControl(main("new"), `[data-project-id="${project.id}"]`),
    );
    fireEvent.change(screen.getByRole("textbox", { name: "Prompt" }), {
      target: { value: "Capability-isolated local session" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start session" }));
    fireEvent.click(
      main("new").querySelector(
        '[data-action="complete-new-session-failure"]',
      ) as HTMLElement,
    );
    expect(
      main("new").querySelector('[data-new-session-state="failure"]'),
    ).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "Settings" }));
    fireEvent.change(screen.getByRole("combobox", { name: "Appearance" }), {
      target: { value: "dark" },
    });
    fireEvent.click(screen.getByRole("checkbox", { name: "Speak responses" }));
    expect(store.getState()).toMatchObject({
      appearance: "dark",
      voicePreferences: { speakResponses: false },
    });

    const opener = screen.getByRole("button", { name: "Switch concept" });
    opener.focus();
    fireEvent.click(opener);
    const closed = nextPopState();
    fireEvent.click(
      screen.getByRole("button", { name: "Select Constellation" }),
    );
    await closed;
    await waitFor(() =>
      expect(
        document.querySelector(".concept-constellation"),
      ).toBeInTheDocument(),
    );

    expect(
      document.querySelectorAll('input[type="file"], input[capture]'),
    ).toHaveLength(0);
    expect(attempts).toEqual([]);
  }, 10_000);
});
