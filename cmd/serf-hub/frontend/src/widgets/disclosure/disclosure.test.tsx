import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { resetDisclosureStoreForTests } from "./disclosureStore";
import { Disclosure } from "./index";

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

test("starts collapsed by default; clicking the summary expands", () => {
  render(
    <Disclosure id="d1" summary="Head" data-testid="d">
      Body
    </Disclosure>,
  );
  expect(screen.queryByText("Body")).toBeNull();
  fireEvent.click(screen.getByText("Head"));
  expect(screen.getByText("Body")).toBeTruthy();
});

test("open state survives remount because it lives in the store", () => {
  const { unmount } = render(
    <Disclosure id="keep" summary="Head">
      Body
    </Disclosure>,
  );
  fireEvent.click(screen.getByText("Head"));
  expect(screen.getByText("Body")).toBeTruthy();
  unmount();
  render(
    <Disclosure id="keep" summary="Head">
      Body
    </Disclosure>,
  );
  expect(screen.getByText("Body")).toBeTruthy(); // still open after remount
});

test("defaultOpen renders open when the store has no entry", () => {
  render(
    <Disclosure id="d2" summary="Head" defaultOpen>
      Body
    </Disclosure>,
  );
  expect(screen.getByText("Body")).toBeTruthy();
});
