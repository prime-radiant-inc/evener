import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { preferenceStorageKey } from "../core/persistence";
import type { PlatformDetectionInput } from "../core/platform";
import type { PreferenceStorage } from "../core/store";
import { App, createRuntimeDiagnosticSink } from "./App";

class TestMediaQueryList extends EventTarget implements MediaQueryList {
  matches = false;
  onchange:
    | ((this: MediaQueryList, ev: MediaQueryListEvent) => unknown)
    | null = null;
  readonly media: string;

  constructor(media: string) {
    super();
    this.media = media;
  }

  addListener(
    callback:
      | ((this: MediaQueryList, ev: MediaQueryListEvent) => unknown)
      | null,
  ) {
    if (callback) this.addEventListener("change", callback as EventListener);
  }

  removeListener(
    callback:
      | ((this: MediaQueryList, ev: MediaQueryListEvent) => unknown)
      | null,
  ) {
    if (callback) this.removeEventListener("change", callback as EventListener);
  }

  dispatchChange(matches: boolean) {
    this.matches = matches;
    this.dispatchEvent(new Event("change"));
  }
}

const platformInput: PlatformDetectionInput = {
  userAgent: "Mozilla/5.0 (Linux; Android 15; Pixel 9)",
  allowOverride: false,
  override: null,
  fallback: "ios",
};

const fullAppCases = (
  [
    {
      platform: "ios",
      platformInput: {
        userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)",
        allowOverride: false,
        override: null,
        fallback: "android",
      },
    },
    { platform: "android", platformInput },
  ] as const
).flatMap(({ platform, platformInput: input }) =>
  (
    [
      { id: "stillwater", label: "Stillwater" },
      { id: "constellation", label: "Constellation" },
      { id: "field-notes", label: "Field Notes" },
    ] as const
  ).map((concept) => ({ ...concept, platform, platformInput: input })),
);

function expectSoleMain(): HTMLElement {
  const mains = screen.getAllByRole("main");
  expect(mains).toHaveLength(1);
  const landmark = mains[0];
  if (!landmark) throw new Error("expected one main landmark");
  expect(landmark.querySelector("main")).toBeNull();
  return landmark;
}

function storage(): PreferenceStorage & { values: Map<string, string> } {
  const values = new Map<string, string>();
  return {
    values,
    getItem: (key) => values.get(key) ?? null,
    setItem: vi.fn((key: string, value: string) => values.set(key, value)),
    removeItem: vi.fn((key: string) => values.delete(key)),
  };
}

let queries: Map<string, TestMediaQueryList>;

function nextPopState(): Promise<PopStateEvent> {
  return new Promise((resolve, reject) => {
    const timeout = window.setTimeout(() => {
      window.removeEventListener("popstate", onPopState);
      reject(new Error("timed out waiting for popstate"));
    }, 1_000);
    const onPopState = (event: PopStateEvent) => {
      window.clearTimeout(timeout);
      window.removeEventListener("popstate", onPopState);
      resolve(event);
    };
    window.addEventListener("popstate", onPopState);
  });
}

beforeEach(() => {
  queries = new Map();
  vi.stubGlobal("matchMedia", (query: string) => {
    let list = queries.get(query);
    if (!list) {
      list = new TestMediaQueryList(query);
      queries.set(query, list);
    }
    return list;
  });
  window.history.replaceState(null, "", "/");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  for (const attribute of [
    "data-appearance",
    "data-reduced-motion",
    "data-platform",
    "data-text-scale",
  ]) {
    document.documentElement.removeAttribute(attribute);
  }
});

describe("App", () => {
  it.each(fullAppCases)(
    "owns one unnested main in the $platform gallery and selected $label App",
    ({ id, label, platform, platformInput: input }) => {
      render(<App platformInput={input} storage={storage()} />);

      expectSoleMain();
      fireEvent.click(screen.getByRole("button", { name: `Select ${label}` }));

      expect(expectSoleMain()).toHaveAttribute("data-route", "sessions");
      expect(document.querySelector(`.concept-${id}`)).toHaveAttribute(
        "data-platform",
        platform,
      );
    },
  );

  it("makes Lab Controls modal, restores its opener after real Back, and focuses the gallery after Reset", async () => {
    render(<App platformInput={platformInput} storage={storage()} />);
    const opener = screen.getByRole("button", { name: "Lab Controls" });
    opener.focus();
    fireEvent.click(opener);

    const close = screen.getByRole("button", { name: "Close Lab Controls" });
    await waitFor(() => expect(close).toHaveFocus());
    expect(screen.getByTestId("foundation-background")).toHaveAttribute(
      "inert",
    );
    expect(opener).toHaveAttribute("inert");

    const closed = nextPopState();
    fireEvent.click(close);
    await closed;
    await waitFor(() => expect(opener).toHaveFocus());

    fireEvent.click(opener);
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Close Lab Controls" }),
      ).toHaveFocus(),
    );
    const reset = nextPopState();
    fireEvent.click(screen.getByRole("button", { name: "Reset prototype" }));
    expect(
      screen.getByRole("button", { name: "Select Stillwater" }),
    ).toHaveFocus();
    await reset;
    expect(
      screen.getByRole("button", { name: "Select Stillwater" }),
    ).toHaveFocus();
  });

  it("uses a no-op diagnostic sink when packaged", () => {
    const logger = { warn: vi.fn() };
    createRuntimeDiagnosticSink(false, logger).report({
      code: "preference-invalid",
      path: "$",
    });
    expect(logger.warn).not.toHaveBeenCalled();
  });

  it("reports malformed preferences in development without exposing raw storage", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const preferenceStorage = storage();
    preferenceStorage.values.set(preferenceStorageKey, '{"raw-secret-marker"');
    render(<App platformInput={platformInput} storage={preferenceStorage} />);

    expect(warn).toHaveBeenCalledOnce();
    expect(warn).toHaveBeenCalledWith("Evener concept diagnostic", {
      code: "preference-invalid",
      path: "$",
    });
    expect(JSON.stringify(warn.mock.calls)).not.toContain("raw-secret-marker");
  });

  it("resolves one platform shell and opens the selected live renderer", () => {
    render(<App platformInput={platformInput} storage={storage()} />);
    expect(document.documentElement).toHaveAttribute(
      "data-platform",
      "android",
    );
    expect(screen.getByRole("main")).toHaveAttribute(
      "data-navigation",
      "material-navigation-bar",
    );

    fireEvent.click(screen.getByRole("button", { name: "Select Stillwater" }));
    expect(screen.queryByTestId("concept-gallery")).not.toBeInTheDocument();
    expect(document.querySelector(".concept-stillwater")).toHaveAttribute(
      "data-platform",
      "android",
    );
    expect(document.querySelector(".concept-stillwater main")).toHaveAttribute(
      "data-route",
      "sessions",
    );
    expect(screen.getByText("Mobile release checklist")).toBeVisible();
    expect(screen.queryByText(/fake session/i)).not.toBeInTheDocument();
  });

  it("applies live system appearance and motion and removes exact listeners", () => {
    const { unmount } = render(
      <App platformInput={platformInput} storage={storage()} />,
    );
    const dark = queries.get("(prefers-color-scheme: dark)");
    const motion = queries.get("(prefers-reduced-motion: reduce)");
    expect(dark).toBeDefined();
    expect(motion).toBeDefined();
    const darkAdd = vi.spyOn(dark as TestMediaQueryList, "addEventListener");
    const motionAdd = vi.spyOn(
      motion as TestMediaQueryList,
      "addEventListener",
    );
    const darkRemove = vi.spyOn(
      dark as TestMediaQueryList,
      "removeEventListener",
    );
    const motionRemove = vi.spyOn(
      motion as TestMediaQueryList,
      "removeEventListener",
    );

    // Remount after spies are installed so the exact callback identities are observable.
    unmount();
    const mounted = render(
      <App platformInput={platformInput} storage={storage()} />,
    );
    act(() => {
      dark?.dispatchChange(true);
      motion?.dispatchChange(true);
    });
    expect(document.documentElement).toHaveAttribute("data-appearance", "dark");
    expect(document.documentElement).toHaveAttribute(
      "data-reduced-motion",
      "true",
    );

    mounted.unmount();
    expect(darkRemove.mock.calls.at(-1)?.[1]).toBe(
      darkAdd.mock.calls.at(-1)?.[1],
    );
    expect(motionRemove.mock.calls.at(-1)?.[1]).toBe(
      motionAdd.mock.calls.at(-1)?.[1],
    );
    act(() => {
      dark?.dispatchChange(false);
      motion?.dispatchChange(false);
    });
    expect(document.documentElement).toHaveAttribute("data-appearance", "dark");
    expect(document.documentElement).toHaveAttribute(
      "data-reduced-motion",
      "true",
    );
  });

  it("combines explicit preferences with system values", () => {
    render(<App platformInput={platformInput} storage={storage()} />);
    fireEvent.click(
      screen.getAllByRole("button", { name: "Lab Controls" })[0] as HTMLElement,
    );
    fireEvent.click(screen.getByRole("radio", { name: "light" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Reduce motion" }));
    act(() => {
      queries.get("(prefers-color-scheme: dark)")?.dispatchChange(true);
    });
    expect(document.documentElement).toHaveAttribute(
      "data-appearance",
      "light",
    );
    expect(document.documentElement).toHaveAttribute(
      "data-reduced-motion",
      "true",
    );
  });

  it("routes Escape through browser history and waits for popstate", () => {
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {});
    render(<App platformInput={platformInput} storage={storage()} />);
    fireEvent.click(screen.getByRole("button", { name: "Lab Controls" }));
    fireEvent.keyDown(window, { key: "Escape" });

    expect(back).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("dialog", { name: "Lab Controls" })).toBeVisible();
  });

  it("clears persistence and returns to an unselected gallery on reset", () => {
    const preferenceStorage = storage();
    render(<App platformInput={platformInput} storage={preferenceStorage} />);
    fireEvent.click(
      screen.getByRole("button", { name: "Select Constellation" }),
    );
    expect(preferenceStorage.setItem).toHaveBeenCalled();
    fireEvent.click(
      screen.getAllByRole("button", { name: "Lab Controls" })[0] as HTMLElement,
    );
    fireEvent.click(screen.getByRole("button", { name: "Reset prototype" }));

    expect(preferenceStorage.removeItem).toHaveBeenCalled();
    for (const button of screen.getAllByRole("button", { name: /^Select / })) {
      expect(button).toHaveAttribute("aria-pressed", "false");
    }
    expect(screen.getAllByRole("article")).toHaveLength(3);
  });

  it("decodes App fixture input and reports structured diagnostics without payloads", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const fixtureInput = { version: 1, secretPayload: "never-log-me" };
    render(
      <App
        platformInput={platformInput}
        storage={storage()}
        fixtureInput={fixtureInput}
      />,
    );

    expect(warn).toHaveBeenCalled();
    expect(JSON.stringify(warn.mock.calls)).toContain("fixture-invalid");
    expect(JSON.stringify(warn.mock.calls)).not.toContain("never-log-me");
    expect(screen.getAllByRole("article")).toHaveLength(3);
  });
});
