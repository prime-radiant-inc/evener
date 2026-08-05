import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { connectionStore } from "../../stores/connection";
import { threadsStore } from "../../stores/threads";
import { SessionPanelPane } from "./SessionPanelPane";

vi.mock("../backToParentAction", () => ({
  BackToParentAction: () => <span data-testid="back-to-parent" />,
}));

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  threadsStore.setState({ threads: new Map() });
});

afterEach(() => cleanup());

test("renders a scaffold loading state before the session model hydrates", () => {
  render(<SessionPanelPane params={{ ref: "ref_a" }} paneId="panel-1" focused kind="tasks" />);

  expect(screen.getByText("Loading session panel…")).toBeTruthy();
  expect(screen.getByTestId("back-to-parent")).toBeTruthy();
});
