import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { FlowOverlay } from "./FlowOverlay";

afterEach(() => {
  cleanup();
});

test("always renders children", () => {
  render(
    <FlowOverlay>
      <div data-testid="content">the list</div>
    </FlowOverlay>,
  );
  expect(screen.getByTestId("content")).toBeTruthy();
});

test("renders the top slot when provided", () => {
  render(
    <FlowOverlay top={<span data-testid="top-content">top</span>}>
      <div />
    </FlowOverlay>,
  );
  expect(screen.getByTestId("top-content")).toBeTruthy();
});

test("renders no top wrapper at all when top is omitted", () => {
  render(
    <FlowOverlay>
      <div />
    </FlowOverlay>,
  );
  expect(screen.queryByTestId("flow-overlay-top")).toBeNull();
});

test("renders the pill slot when provided", () => {
  render(
    <FlowOverlay pill={<span data-testid="pill-content">pill</span>}>
      <div />
    </FlowOverlay>,
  );
  expect(screen.getByTestId("pill-content")).toBeTruthy();
});

test("renders no pill wrapper at all when pill is omitted", () => {
  render(
    <FlowOverlay>
      <div />
    </FlowOverlay>,
  );
  expect(screen.queryByTestId("flow-overlay-pill")).toBeNull();
});
