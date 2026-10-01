import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import {
  DISCLOSURE_STORAGE_KEY,
  DISCLOSURE_STORAGE_LIMIT,
  DisclosurePersistenceContext,
  resetDisclosureStoreForTests,
} from "./disclosureStore";
import { Disclosure } from "./index";

beforeEach(() => installLocalStorage(new MemoryStorage()));
afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
  vi.restoreAllMocks();
});

function renderChoice(scope: string | null = null) {
  return render(
    <DisclosurePersistenceContext.Provider value={scope}>
      <Disclosure id={"source:owner\0task-1"} summary="Inspect task">
        Task details
      </Disclosure>
    </DisclosurePersistenceContext.Provider>,
  );
}

test("only an opted-in view retains a disclosure across fresh in-memory owners", () => {
  const view = renderChoice();
  fireEvent.click(screen.getByText("Inspect task"));
  view.unmount();
  resetDisclosureStoreForTests();
  const second = renderChoice("source:owner/tasks");
  expect(screen.queryByText("Task details")).toBeNull();
  fireEvent.click(screen.getByText("Inspect task"));
  second.unmount();
  resetDisclosureStoreForTests();
  const third = renderChoice("source:owner/tasks");
  expect(screen.getByText("Task details")).toBeTruthy();
  third.unmount();
  resetDisclosureStoreForTests();
  renderChoice("source:other/tasks");
  expect(screen.queryByText("Task details")).toBeNull();
});

test("blocked and malformed persistence never prevent disclosure interaction", () => {
  localStorage.setItem(DISCLOSURE_STORAGE_KEY, "{broken");
  vi.spyOn(localStorage, "setItem").mockImplementation(() => {
    throw new Error("full");
  });
  renderChoice("source:owner/tasks");
  fireEvent.click(screen.getByText("Inspect task"));
  expect(screen.getByText("Task details")).toBeTruthy();
  fireEvent.click(screen.getByText("Inspect task"));
  expect(screen.queryByText("Task details")).toBeNull();
});

test("the bounded disclosure record drops oldest choices only on an explicit new choice", () => {
  const saved = Object.fromEntries(
    Array.from({ length: DISCLOSURE_STORAGE_LIMIT }, (_, index) => [`old-${index}`, true]),
  );
  localStorage.setItem(DISCLOSURE_STORAGE_KEY, JSON.stringify(saved));
  const writes = vi.spyOn(localStorage, "setItem");
  renderChoice("source:owner/tasks");
  expect(writes).not.toHaveBeenCalled();
  fireEvent.click(screen.getByText("Inspect task"));
  const next = JSON.parse(localStorage.getItem(DISCLOSURE_STORAGE_KEY) ?? "{}");
  expect(Object.keys(next)).toHaveLength(DISCLOSURE_STORAGE_LIMIT);
  expect(next["old-0"]).toBeUndefined();
  expect(next[JSON.stringify(["source:owner/tasks", "source:owner\0task-1"])]).toBe(true);
});
