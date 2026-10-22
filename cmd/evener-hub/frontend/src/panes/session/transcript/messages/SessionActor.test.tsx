import type { ItemModel } from "@evener/appwire-client";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { SteeringItem } from "./SteeringItem";
import { UserMessageView } from "./UserMessageItem";

afterEach(cleanup);

const base: ItemModel = { id: "item_1", turnId: "turn_1", type: "userMessage", text: "same input" };

test("SessionActor distinguishes manager input from ordinary human input", () => {
  render(<UserMessageView item={{ ...base, actor: { kind: "human" } }} />);
  expect(screen.getByText("You")).toBeTruthy();
  cleanup();
  render(<UserMessageView item={{ ...base, actor: { kind: "manager", managerRef: "local:manager" } }} />);
  expect(screen.getByText(/Manager.*local:manager/)).toBeTruthy();
  cleanup();
  render(
    <SteeringItem
      item={{
        ...base,
        id: "steer_1",
        type: "steering",
        source: "user",
        actor: { kind: "manager", managerRef: "local:manager" },
      }}
      sessionRef="local:worker"
      turn={{ id: "turn_1", status: "completed", items: [] }}
      live={false}
    />,
  );
  expect(screen.getByText(/Manager.*local:manager/)).toBeTruthy();
});
