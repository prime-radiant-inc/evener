import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadStartResponse } from "../../protocol/types.gen";
import { ClientProvider } from "../../shell/clientContext";
import { Toast } from "../../widgets";
import Spawn from "./Spawn";

const NO_CAPABILITIES: ThreadCapabilities = {
  send: false,
  steer: false,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  queue: false,
  goal: false,
  rename: false,
};

function threadWithRef(ref: string): Thread {
  return {
    id: ref.includes(":") ? ref.slice(ref.indexOf(":") + 1) : ref,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "local",
    serf: { ref, capabilities: NO_CAPABILITIES, queue: {} },
  };
}

function startResponse(ref: string): ThreadStartResponse {
  return { thread: threadWithRef(ref), turn: { id: "turn_1", itemsView: "full", status: "idle" } };
}

function renderSpawn(client: FakeClient) {
  return render(
    <ClientProvider client={client}>
      <Spawn params={{}} paneId="spawn-1" focused={true} />
      <Toast />
    </ClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
});

test("renders a prompt field, a working-directory picker, and a Spawn button", () => {
  renderSpawn(new FakeClient("ready"));
  expect(screen.getByRole("textbox", { name: "Prompt" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Spawn" })).toBeTruthy();
});

test("spawning a prompt starts a real session and routes to /s/{ref}", async () => {
  const user = userEvent.setup();
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:abc123"));
  renderSpawn(fake);

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
  await user.click(screen.getByRole("button", { name: "Spawn" }));

  // The qualified ref becomes the session URL (":" percent-encoded).
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const startCall = fake.calls.find((c) => c.method === "thread/start");
  expect(startCall?.params).toMatchObject({ input: [{ type: "text", text: "do the thing" }] });
});

test("a failed spawn surfaces an error toast and does not navigate", async () => {
  const user = userEvent.setup();
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => Promise.reject(new Error("spawner not configured")));
  renderSpawn(fake);

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "go");
  await user.click(screen.getByRole("button", { name: "Spawn" }));

  expect(await screen.findByText(/spawn failed: spawner not configured/i)).toBeTruthy();
  expect(window.location.pathname).toBe("/");
});
