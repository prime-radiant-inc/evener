import { hydrateThread, type TranscriptAuthorship } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { capability, manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { act, render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { initNavigation, resetNavigationStoreForTests } from "../stores/navigation/store";
import { ComicApp } from "./ComicApp";
import { ComicPanel } from "./ComicPanel";
import { composeComicRows } from "./layout";
import { createComicMeasurer, loadComicFonts } from "./measure";
import { type ComicEntry, projectComicConversation } from "./projection";

const browser = vi.hoisted(() => ({ client: null as FakeClient | null }));
vi.mock("../shell/useBrowserConnection", () => ({
  useBrowserConnection: () => ({ client: browser.client, onClientReplaced: () => {} }),
}));

function entry(fields: Partial<ComicEntry> = {}): ComicEntry {
  return {
    id: "entry-1",
    sourceItemId: "source-1",
    sourceIndex: 0,
    kind: "dialogue",
    speaker: "human",
    sourceLabel: "Human",
    text: "Original\t words\n\nremain",
    streaming: false,
    persona:
      fields.speaker === undefined || fields.speaker === "human"
        ? { key: "human", character: "anna", name: "Anna", badge: "human" }
        : undefined,
    ...fields,
  } as ComicEntry;
}

function panel(value: ComicEntry, start = 0, end = value.text.length) {
  return (
    <ComicPanel
      entry={value}
      slice={{ entryId: value.id, start, end }}
      composition="wide"
      scene="room"
      expression="neutral"
    />
  );
}

test("selectable balloon text is the literal UTF-16 slice", () => {
  const value = entry();
  const { container } = render(panel(value, 9, 16));
  expect(container.querySelector("[data-comic-text]")?.textContent).toBe(value.text.slice(9, 16));
  expect(container.querySelector("[data-comic-scene]")?.getAttribute("aria-hidden")).toBe("true");
  expect(container.querySelectorAll("[data-comic-scene] img[alt='']").length).toBeGreaterThan(0);
});

test("thought clouds have a distinct trail and an accessible thought identity", () => {
  const thought = entry({
    kind: "thought",
    speaker: "assistant",
    sourceLabel: "Thought",
    persona: { key: "assistant", character: "xeno", name: "Xeno", badge: "assistant" },
  });
  const cloud = render(panel(thought));
  expect(cloud.container.querySelector("[data-comic-balloon]")?.getAttribute("data-comic-balloon")).toBe("thought");
  expect(cloud.container.querySelectorAll("[data-comic-thought-trail] > span")).toHaveLength(2);
  expect(screen.getByRole("article").getAttribute("aria-label")).toMatch(/thought/i);
  cloud.unmount();
  const speech = render(panel(entry()));
  expect(speech.container.querySelector("[data-comic-balloon]")?.getAttribute("data-comic-balloon")).toBe("speech");
  expect(speech.container.querySelector("[data-comic-thought-trail]")).toBeNull();
});

test("proven inheritance labels the original author without changing thought or continuation text", () => {
  const value = entry({
    kind: "thought",
    speaker: "assistant",
    inherited: true,
    text: "Before ORIGINAL_THOUGHT after",
    persona: { key: "original", character: "tiki", name: "Original", badge: "dlg_original" },
  });
  const rendered = render(panel(value, 7, 23));
  const labels = () => ({
    accessible: screen.getByRole("article").getAttribute("aria-label"),
    visible: rendered.container.querySelector("[data-comic-identity]")?.textContent,
  });
  const inheritedLabels = labels();
  for (const label of Object.values(inheritedLabels)) {
    expect(label).toContain(value.persona?.name);
    expect(label).toContain(value.persona?.badge);
  }
  expect(rendered.container.querySelector("[data-comic-text]")?.textContent).toBe("ORIGINAL_THOUGHT");
  expect(rendered.container.querySelector("[data-comic-balloon]")?.getAttribute("data-comic-balloon")).toBe("thought");
  expect(rendered.container.querySelectorAll("[data-comic-thought-trail] > span")).toHaveLength(2);
  let freshLabels: ReturnType<typeof labels> | undefined;
  for (const inherited of [false, undefined]) {
    rendered.rerender(panel({ ...value, inherited }, 7, 23));
    const current = labels();
    for (const key of ["accessible", "visible"] as const) {
      expect(current[key]).not.toBe(inheritedLabels[key]);
      expect(current[key]).toContain(value.persona?.name);
      expect(current[key]).toContain(value.persona?.badge);
    }
    if (freshLabels) expect(current).toEqual(freshLabels);
    freshLabels = current;
    expect(rendered.container.querySelector("[data-comic-text]")?.textContent).toBe("ORIGINAL_THOUGHT");
  }
  rendered.rerender(panel(value, 0, 23));
  expect(labels().accessible).not.toBe(inheritedLabels.accessible);
  expect(labels().visible).not.toBe(inheritedLabels.visible);
  expect(rendered.container.querySelector("[data-comic-text]")?.textContent).toBe(value.text.slice(0, 23));
  rendered.rerender(
    panel(
      entry({ kind: "dialogue", speaker: value.speaker, inherited: true, text: value.text, persona: value.persona }),
      7,
      23,
    ),
  );
  expect(labels().accessible).not.toBe(inheritedLabels.accessible);
  expect(labels().visible).toBe(inheritedLabels.visible);
  expect(rendered.container.querySelector("[data-comic-thought-trail]")).toBeNull();
  expect(rendered.container.querySelector("[data-comic-text]")?.textContent).toBe("ORIGINAL_THOUGHT");
});

test("complete delegate identity stays available on narrow panels", () => {
  const value = entry({
    kind: "caption",
    speaker: "source",
    sourceLabel: "Source",
    text: "Delivered",
    persona: {
      key: "[owner,delegate]",
      character: "tiki",
      name: "A Very Long Delegate Name with Spaces",
      badge: "dlg_exact_identifier_12345678901234567890",
      ownerRef: "owner",
    },
    delegateResult: { kind: "earlierReply", status: "failed" },
  });
  const { container } = render(panel(value));
  expect(container.querySelector("[data-comic-identity]")?.textContent).toContain(value.persona?.name);
  expect(container.querySelector("[data-comic-identity]")?.textContent).toContain(value.persona?.badge);
  expect(container.querySelector("[data-comic-source]")?.textContent).toContain(value.sourceLabel);
  expect(container.querySelector("[data-comic-source]")?.textContent).toContain(value.delegateResult?.status);
  expect(container.querySelector("img[src$='tiki-neutral.png']")).not.toBeNull();
});

test.each([
  undefined,
  { author: { kind: "unknown" }, inherited: false },
  { author: { kind: "system" }, inherited: false },
  { author: { kind: "agent", sessionRef: "unqualified" }, inherited: false },
] satisfies (TranscriptAuthorship | undefined)[])(
  "unproven reasoning author %# keeps literal caption and background without a character",
  (authorship) => {
    const text = "  Original thought\n\n🧭 stays literal.  ";
    const model = hydrateThread(
      {
        thread: wireThread("local:root", {
          turns: [
            {
              id: "turn",
              status: "completed",
              itemsView: "full",
              items: [{ type: "reasoning", id: "thought", turnId: "turn", text, authorship }],
            },
          ],
        }),
      },
      "local:root",
      0,
    );
    const entries = projectComicConversation(model);
    expect(entries).toHaveLength(1);
    for (const value of entries) {
      expect(value.persona).toBeUndefined();
      const { container } = render(panel(value));
      expect(container.querySelector("[data-comic-text]")?.textContent).toBe(text);
      expect(container.querySelector("[data-comic-balloon]")?.getAttribute("data-comic-balloon")).toBe("caption");
      expect(container.querySelector("[data-comic-scene]")?.getAttribute("data-comic-scene")).toBe("room");
      expect(container.querySelectorAll("[data-comic-scene] img")).toHaveLength(0);
    }
  },
);

test("equal delegate names and IDs retain their distinct owners in comic panels", () => {
  const owners = ["beta:root", "delta:root"];
  const name = "Review";
  const delegateId = "dlg_shared";
  const model = hydrateThread(
    {
      thread: wireThread("local:root", {
        turns: [
          {
            id: "turn",
            status: "completed",
            itemsView: "full",
            items: owners.map((ownerRef, index) => ({
              type: "agentMessage" as const,
              id: `reply-${index}`,
              turnId: "turn",
              text: `Reply from ${ownerRef}`,
              authorship: {
                author: {
                  kind: "agent" as const,
                  sessionRef: ownerRef.replace(":root", ":child"),
                  ownerRef,
                  delegateId,
                  delegateName: name,
                },
                inherited: true,
              },
            })),
          },
        ],
      }),
    },
    "local:root",
    0,
  );
  const entries = projectComicConversation(model);
  expect(entries).toHaveLength(2);
  expect(entries.map((value) => value.persona?.character)).toEqual(["denise", "denise"]);
  const { container } = render(entries.map((value) => <div key={value.id}>{panel(value)}</div>));
  const articles = [...container.querySelectorAll("article")];
  expect(articles).toHaveLength(2);
  articles.forEach((article, index) => {
    for (const identity of [
      article.getAttribute("aria-label"),
      article.querySelector("[data-comic-identity]")?.textContent,
    ]) {
      expect(identity).toContain(name);
      expect(identity).toContain(delegateId);
      expect(identity).toContain(owners[index]);
    }
    expect(article.querySelector("img[src$='denise-neutral.png']")).not.toBeNull();
    expect(article.querySelector("[data-comic-text]")?.textContent).toBe(`Reply from ${owners[index]}`);
  });
});

test("turn-error captions keep their canonical turn anchor", () => {
  const value = entry({
    id: "turn-error",
    sourceItemId: undefined,
    sourceIndex: undefined,
    kind: "caption",
    speaker: "source",
    sourceTurnId: "turn_real_42",
    sourceLabel: "Turn error",
    text: "Failure",
  });
  const { container } = render(panel(value));
  expect(container.querySelector("[data-comic-turn-id]")?.getAttribute("data-comic-turn-id")).toBe("turn_real_42");
});

test("image-only and content-free thought panels contain no fabricated text", () => {
  const image = entry({
    kind: "images",
    speaker: "source",
    sourceLabel: "read_file",
    text: "",
    images: [{ src: "data:image/png;base64,iVBORw0KGgo=", name: "evidence.png" }],
  });
  const rendered = render(panel(image));
  expect(rendered.container.querySelector("[data-comic-text]")).toBeNull();
  expect(screen.getByRole("button", { name: /image 1/i })).toBeTruthy();
  rendered.unmount();
  const thought = render(panel(entry({ kind: "thought", text: "" })));
  expect(thought.container.querySelector("[data-comic-text]")?.textContent).toBe("");
});

test("a mixed message preserves every text unit and renders one ordered image gallery after its last slice", () => {
  const value = entry({
    text: "Before\t👩🏽‍💻\n\nafter. ".repeat(8),
    images: [
      { src: "data:image/png;base64,iVBORw0KGgo=", name: "first.png" },
      { src: "data:image/png;base64,iVBORw0KGgp=", name: "second.png" },
    ],
  });
  const panels = composeComicRows([value], 1200, (text) => text.length <= 18).flatMap((row) => row.panels);
  expect(panels.length).toBeGreaterThan(2);
  let offset = 0;
  for (const { slice } of panels) {
    expect(slice.start).toBe(offset);
    expect(slice.end).toBeGreaterThan(offset);
    offset = slice.end;
  }
  expect(offset).toBe(value.text.length);

  const { container } = render(
    <div>
      {panels.map(({ slice, composition }) => (
        <ComicPanel
          key={slice.start}
          entry={value}
          slice={slice}
          composition={composition}
          scene="room"
          expression="neutral"
        />
      ))}
    </div>,
  );
  const articles = Array.from(container.querySelectorAll("article"));
  expect(articles.map((article) => article.querySelector("[data-comic-text]")?.textContent).join("")).toBe(value.text);
  expect(
    articles
      .slice(0, -1)
      .every((article) => article.querySelectorAll("[data-testid='image-gallery-thumb']").length === 0),
  ).toBe(true);
  const last = articles.at(-1);
  expect(
    Array.from(last?.querySelectorAll("[data-comic-text], [data-testid='image-gallery-thumb'] img") ?? [], (node) =>
      node instanceof HTMLImageElement ? node.getAttribute("src") : node.textContent,
    ),
  ).toEqual([value.text.slice(panels.at(-1)?.slice.start), ...(value.images?.map((image) => image.src) ?? [])]);
  expect(
    Array.from(container.querySelectorAll("[data-testid='image-gallery-caption']"), (caption) => caption.textContent),
  ).toEqual(["first.png", "second.png"]);
});

test("an image-only user message retains its gallery without inventing text", () => {
  const value = entry({ text: "", images: [{ src: "data:image/png;base64,iVBORw0KGgo=", name: "user.png" }] });
  const rows = composeComicRows([value], 1200, () => true);
  expect(rows.map((row) => row.panels)).toEqual([
    [{ slice: { entryId: value.id, start: 0, end: 0 }, composition: "wide" }],
  ]);
  const { container } = render(panel(value));
  expect(container.querySelector("[data-comic-text]")?.textContent).toBe("");
  expect(container.querySelectorAll("[data-testid='image-gallery-thumb']")).toHaveLength(1);
  expect(container.querySelector("[data-testid='image-gallery-caption']")?.textContent).toBe("user.png");
});

test("stage pose is stable for an entry and presentation never infers a result", () => {
  const value = entry({ id: "stable" });
  const rendered = render(panel(value));
  const pose = rendered.container.querySelector("[data-comic-scene] img")?.getAttribute("src");
  rendered.rerender(panel({ ...value, text: "Everything failed." }));
  expect(rendered.container.querySelector("[data-comic-scene] img")?.getAttribute("src")).toBe(pose);
  rendered.rerender(
    <ComicPanel
      entry={value}
      slice={{ entryId: value.id, start: 0, end: value.text.length }}
      composition="wide"
      scene="field"
      expression="angry"
    />,
  );
  expect(rendered.container.querySelector("[data-comic-scene]")?.getAttribute("data-comic-scene")).toBe("field");
  expect(rendered.container.querySelector("[data-comic-scene] img")?.getAttribute("src")).toContain("anna-angry.png");
});

test("DOM probe checks both balloon geometries and can be disposed", () => {
  const root = document.createElement("div");
  document.body.append(root);
  const measure = createComicMeasurer(root);
  expect(root.querySelector("[data-comic-probe]")).not.toBeNull();
  measure.fits("words", "wide", "thought");
  expect(root.querySelector("[data-comic-probe]")?.getAttribute("data-comic-kind")).toBe("thought");
  measure.dispose();
  expect(root.querySelector("[data-comic-probe]")).toBeNull();
  root.remove();
});

test("font loading checks regular and bold faces explicitly", async () => {
  const original = Object.getOwnPropertyDescriptor(document, "fonts");
  const checked: string[] = [];
  Object.defineProperty(document, "fonts", {
    configurable: true,
    value: {
      load: async (face: string) => {
        checked.push(face);
        return [];
      },
    },
  });
  try {
    expect(await loadComicFonts()).toBe("unavailable");
    expect(checked.some((face) => face.includes("400"))).toBe(true);
    expect(checked.some((face) => face.includes("700"))).toBe(true);
  } finally {
    if (original) Object.defineProperty(document, "fonts", original);
    else Reflect.deleteProperty(document, "fonts");
  }
});

test("comic root scopes the light paper theme without changing the global hub theme", () => {
  const mounted = render(<ComicApp />);
  try {
    expect(mounted.container.querySelector("main")?.getAttribute("data-theme")).toBe("light");
    expect(document.documentElement.getAttribute("data-theme")).not.toBe("light");
  } finally {
    mounted.unmount();
  }
});

test("missing fonts announce setup while conversation controls stay usable", async () => {
  const saved = Object.getOwnPropertyDescriptor(document, "fonts");
  Object.defineProperty(document, "fonts", { configurable: true, value: { load: async () => [] } });
  const client = new FakeClient("ready");
  browser.client = client;
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest")
      return wireSnapshot(
        params,
        manifest({ sections: { live: { count: 1 }, needs_you: { count: 0 }, pin_sections: { count: 0 } } }),
      );
    if (params.resource === "section")
      return wireSnapshot(params, {
        sessions: [{ ref: "local:one", title: "One", children: [] }],
        remaining: 0,
        truncated: false,
      });
    if (params.resource === "catalog") return wireSnapshot(params, { projects: [], remaining: 0 });
    throw new Error(`unexpected ${params.resource}`);
  });
  await act(async () => {
    initNavigation(client, capability());
    for (let i = 0; i < 16; i += 1) await Promise.resolve();
  });
  window.history.replaceState(null, "", "/comic");
  const mounted = render(<ComicApp />);
  try {
    await act(async () => {
      for (let i = 0; i < 16; i += 1) await Promise.resolve();
    });
    expect(screen.getByRole("status", { name: /font/i })).toBeTruthy();
    await act(async () => {
      screen.getByRole("button", { name: "One" }).click();
    });
    expect(window.location.pathname).not.toBe("/comic");
  } finally {
    mounted.unmount();
    browser.client = null;
    resetNavigationStoreForTests();
    if (saved) Object.defineProperty(document, "fonts", saved);
    else Reflect.deleteProperty(document, "fonts");
  }
});
