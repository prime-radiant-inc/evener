import type { FakeClient } from "@evener/appwire-client/testing/fakeClient";

// This opt-in shellguard fixture seeds actual same-document browser history
// before AppShell mounts. It never simulates Navigation API entry identity.
export async function prepareSettingsHistory(mode: string): Promise<void> {
  if (!["native", "indexed", "legacy"].includes(mode)) throw new Error(`Unknown history fixture: ${mode}`);
  const nativeNavigation = window.navigation;
  if (!nativeNavigation) throw new Error("Settings browser acceptance requires Chrome's native Navigation API");
  const urls = ["/", "/settings/theme?history=first", "/settings/agents-md", "/settings/theme?history=forward"];
  for (const [index, url] of urls.entries()) {
    const state = mode === "indexed" ? { evenerNavigationIndex: index } : { fixtureEntry: index };
    if (index === 0) history.replaceState(state, "", url);
    else history.pushState(state, "", url);
  }
  await new Promise<void>((resolve) => {
    window.addEventListener("popstate", () => resolve(), { once: true });
    history.back();
  });
  // Explicit compatibility test only: the browser still traverses real history,
  // but production code cannot see Navigation API. Native cases never mask it.
  if (mode === "legacy" || mode === "indexed") {
    Object.defineProperty(window, "navigation", { configurable: true, value: undefined });
  }
  const writes: string[] = [];
  for (const method of ["pushState", "replaceState"] as const) {
    const original = history[method].bind(history);
    history[method] = (data: unknown, unused: string, url?: string | URL | null) => {
      writes.push(method);
      return original(data, unused, url);
    };
  }
  const traversals: { url: string; key: string; index: number; state: unknown; trusted: boolean }[] = [];
  window.addEventListener(
    "popstate",
    (event) => {
      const entry = nativeNavigation.currentEntry;
      if (!entry) throw new Error("Native currentEntry disappeared");
      traversals.push({
        url: location.pathname + location.search,
        key: entry.key,
        index: entry.index,
        state: history.state,
        trusted: event.isTrusted,
      });
    },
    true,
  );
  const routing = await import("../shell/routing");
  const target = window as typeof window & { settingsHistoryWitness?: () => unknown };
  target.settingsHistoryWitness = () => ({
    mode,
    nativeAvailable: nativeNavigation instanceof Navigation,
    productNavigationAvailable: window.navigation !== undefined,
    current: { key: nativeNavigation.currentEntry?.key, index: nativeNavigation.currentEntry?.index },
    entries: nativeNavigation.entries().map((entry) => ({ key: entry.key, index: entry.index, url: entry.url })),
    address: location.pathname + location.search,
    accepted: routing.navigationLocation().pathname + routing.navigationLocation().search,
    writes: [...writes],
    traversals: [...traversals],
  });
}

export function scriptSettingsHistory(fake: FakeClient): void {
  fake.on("evener/settings/agentsDoc/get", () => ({
    path: "/fixture/AGENTS.md",
    exists: true,
    content: "browser-history-baseline",
  }));
  // A write is a test failure, not a successful fake save. No real preferences
  // or files are touched, and the acceptance runner checks the call log too.
  fake.on("evener/settings/agentsDoc/set", () => {
    throw new Error("History acceptance must never save a document");
  });
  const target = window as typeof window & { settingsHistoryCalls?: () => unknown };
  target.settingsHistoryCalls = () => fake.calls.map(({ method }) => method);
}
