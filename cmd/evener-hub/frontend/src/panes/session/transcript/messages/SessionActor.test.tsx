import type { ItemModel } from "@evener/appwire-client";
import { cleanup, render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { SteeringItem } from "./SteeringItem";
import { UserMessageView } from "./UserMessageItem";

const base: ItemModel = { id: "item_1", turnId: "turn_1", type: "userMessage", text: "same input" };

test("SessionActor distinguishes manager input from ordinary human input", () => {
  render(<UserMessageView item={{ ...base, actor: { kind: "human" } }} />);
  expect(screen.getByText("You")).toBeTruthy();
  cleanup();
  render(<UserMessageView item={{ ...base, actor: { kind: "manager", managerRef: "local:manager-origin" } }} />);
  expect(screen.getByText(/Manager.*local:manager-origin/)).toBeTruthy();
  cleanup();
  render(
    <SteeringItem
      item={{
        ...base,
        id: "steer_1",
        type: "steering",
        source: "user",
        actor: { kind: "manager", managerRef: "local:manager-origin" },
      }}
      sessionRef="local:worker"
      turn={{ id: "turn_1", status: "completed", items: [] }}
      live={false}
    />,
  );
  expect(screen.getByText(/Manager.*local:manager-origin/)).toBeTruthy();
});
