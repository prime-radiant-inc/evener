import type { LaunchConfigLayer, LaunchConfigResolved, LaunchOptionSchemaResponse } from "@evener/appwire-client";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { Toast } from "../../../../widgets";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { LaunchConfigForm } from "./LaunchConfigForm";

// LaunchConfigForm's own contract when the SAME panes re-reads the SAME host's
// layer while the form is mounted (component 07b): a host's launch config can
// change under a mounted pane, and the pane re-reads it through the refresh path
// - a referentially new `current` from the same owner. That re-read must
// converge the form on the host's NEW values without ever silently taking away
// what the user is typing.

const SCHEMA: LaunchOptionSchemaResponse = {
  options: [
    {
      field: "agent",
      wireField: "agent",
      label: "Agent",
      group: "Agent",
      kind: "text",
      perLaunch: true,
      defaultableLayers: ["global", "project"],
    },
  ],
};

const RESOLVED: LaunchConfigResolved = { effective: {}, layers: {}, provenance: {} };

const OWNER = { instance: "beta" };

function renderForm(current: LaunchConfigLayer) {
  return render(
    <LaunchConfigForm
      options={SCHEMA.options}
      layer="global"
      current={current}
      successToast="Launch defaults saved"
      validatePath={async (path) => ({ valid: true, path })}
      host="beta"
      draftOwner={OWNER}
      onSave={async () => RESOLVED}
    />,
  );
}

function rerenderForm(rerender: ReturnType<typeof render>["rerender"], current: LaunchConfigLayer): void {
  rerender(
    <LaunchConfigForm
      options={SCHEMA.options}
      layer="global"
      current={current}
      successToast="Launch defaults saved"
      validatePath={async (path) => ({ valid: true, path })}
      host="beta"
      draftOwner={OWNER}
      onSave={async () => RESOLVED}
    />,
  );
}

afterEach(() => {
  cleanup();
  resetToastStoreForTests();
});

test("a re-read that brings different values re-seeds a form the user has not edited", () => {
  const { rerender } = renderForm({ agent: "beta-agent" });
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("beta-agent");

  // The pane re-reads the same host: a new `current` from the SAME owner, with
  // the newer value another client saved.
  rerenderForm(rerender, { agent: "beta-agent-2" });

  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("beta-agent-2");
  expect(screen.queryByText(/changed on the host while you were editing/)).toBeNull();
});

test("a re-read that brings DIFFERENT values keeps the user's unsaved draft and says so", async () => {
  const { rerender } = renderForm({ agent: "beta-agent" });
  const user = userEvent.setup();
  const agent = screen.getByLabelText("Agent") as HTMLInputElement;
  await user.clear(agent);
  await user.type(agent, "typed-on-beta");

  rerenderForm(rerender, { agent: "beta-agent-2" });

  // The draft survives untouched...
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("typed-on-beta");
  // ...and the user is told the host's values moved, rather than being left to
  // overwrite them silently with a later Save.
  expect(screen.getByText(/changed on the host while you were editing/)).toBeTruthy();
});

test("the conflict notice's own button adopts the host's values and drops the draft", async () => {
  const { rerender } = renderForm({ agent: "beta-agent" });
  const user = userEvent.setup();
  const agent = screen.getByLabelText("Agent") as HTMLInputElement;
  await user.clear(agent);
  await user.type(agent, "typed-on-beta");

  rerenderForm(rerender, { agent: "beta-agent-2" });
  expect(screen.getByText(/changed on the host while you were editing/)).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Use the host's values" }));

  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("beta-agent-2");
  expect(screen.queryByText(/changed on the host while you were editing/)).toBeNull();
});

test("a same-content re-read leaves the notice and the form alone", async () => {
  const { rerender } = renderForm({ agent: "beta-agent" });
  const user = userEvent.setup();
  const agent = screen.getByLabelText("Agent") as HTMLInputElement;
  await user.clear(agent);
  await user.type(agent, "typed-on-beta");

  // A fresh object carrying the SAME values (every read produces one) is not a
  // change: no notice, and the draft stays.
  rerenderForm(rerender, { agent: "beta-agent" });

  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("typed-on-beta");
  expect(screen.queryByText(/changed on the host while you were editing/)).toBeNull();
});

// The save path is untouched: an edited draft still wins when the user chooses
// to save it, exactly as today - the notice is information, not a lock.
test("Save still emits the user's draft after a conflicting re-read", async () => {
  const onSave = vi.fn(async (_config: LaunchConfigLayer) => RESOLVED);
  const { rerender } = render(
    <>
      <Toast />
      <LaunchConfigForm
        options={SCHEMA.options}
        layer="global"
        current={{ agent: "beta-agent" }}
        successToast="Launch defaults saved"
        validatePath={async (path) => ({ valid: true, path })}
        host="beta"
        draftOwner={OWNER}
        onSave={onSave}
      />
    </>,
  );
  const user = userEvent.setup();
  const agent = screen.getByLabelText("Agent") as HTMLInputElement;
  await user.clear(agent);
  await user.type(agent, "typed-on-beta");

  rerender(
    <>
      <Toast />
      <LaunchConfigForm
        options={SCHEMA.options}
        layer="global"
        current={{ agent: "beta-agent-2" }}
        successToast="Launch defaults saved"
        validatePath={async (path) => ({ valid: true, path })}
        host="beta"
        draftOwner={OWNER}
        onSave={onSave}
      />
    </>,
  );

  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
  expect(onSave).toHaveBeenCalledTimes(1);
  expect(onSave.mock.calls[0]?.[0]).toEqual({ agent: "typed-on-beta" });
});
