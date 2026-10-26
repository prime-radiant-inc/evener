import { buildEntityView, hydrateThread, parseActivityTree } from "@evener/appwire-client";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { subagentOutcomesResponse } from "@evener/appwire-client/testing/subagentWireFixtures";
import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { ComicSubchatHeader } from "./ComicSubchatHeader";
import { projectComicDelegates } from "./delegates";

test("opens the authoritative child route and returns focus on collapse without inline actions", () => {
  const model = hydrateThread({ thread: wireThread("local:root") }, "local:root", 0);
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
  const mount = render(<ComicSubchatHeader strip={strip} expanded onToggle={toggle} onOpen={open} />);
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
