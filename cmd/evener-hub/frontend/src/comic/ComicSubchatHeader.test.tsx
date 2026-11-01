import { buildEntityView, hydrateThread, parseActivityTree } from "@evener/appwire-client";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { subagentOutcomesResponse } from "@evener/appwire-client/testing/subagentWireFixtures";
import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { ComicSubchatHeader } from "./ComicSubchatHeader";
import { projectComicDelegates } from "./delegates";

test("opens the authoritative child route and returns focus on collapse without inline actions", () => {
  const model = hydrateThread({ thread: wireThread("local:root", { sessionId: "root" }) }, "local:root", 0);
  const entities = buildEntityView({
    sessionRef: model.ref,
    tree: parseActivityTree(subagentOutcomesResponse().data) ?? undefined,
    turns: [],
    stale: false,
    ended: false,
  });
  const strip = projectComicDelegates(model, entities)[0]!;
  const toggle = vi.fn();
  const open = vi.fn();
  const mount = render(<ComicSubchatHeader strip={strip} updateCount={0} expanded onToggle={toggle} onOpen={open} />);
  const buttons = screen.getAllByRole("button");
  expect(buttons).toHaveLength(2);
  fireEvent.click(buttons[1]!);
  expect(open).toHaveBeenCalledWith(strip.childRef);
  fireEvent.click(buttons[0]!);
  expect(toggle).toHaveBeenCalledOnce();
  expect(document.activeElement).toBe(buttons[0]);
  expect(buttons[0]?.getAttribute("aria-expanded")).toBe("true");
  expect(mount.container.querySelector("[data-comic-delegate]")?.getAttribute("data-comic-delegate")).toBe(strip.key);
  expect(mount.container.querySelectorAll("textarea,input,form")).toHaveLength(0);
});

test("reports the strip's folded update count without adding actions", () => {
  const model = hydrateThread({ thread: wireThread("local:root", { sessionId: "root" }) }, "local:root", 0);
  const entities = buildEntityView({
    sessionRef: model.ref,
    tree: parseActivityTree(subagentOutcomesResponse().data) ?? undefined,
    turns: [],
    stale: false,
    ended: false,
  });
  const strip = projectComicDelegates(model, entities)[0]!;
  const mount = render(
    <ComicSubchatHeader strip={strip} updateCount={3} expanded onToggle={() => {}} onOpen={() => {}} />,
  );
  const badge = mount.container.querySelector("[data-comic-updates]");
  expect(badge?.getAttribute("data-comic-updates")).toBe("3");
  expect(badge?.textContent).not.toBe("");
  expect(screen.getAllByRole("button")).toHaveLength(2);
  const quiet = render(
    <ComicSubchatHeader strip={strip} updateCount={0} expanded onToggle={() => {}} onOpen={() => {}} />,
  );
  expect(quiet.container.querySelector("[data-comic-updates]")).toBeNull();
});
