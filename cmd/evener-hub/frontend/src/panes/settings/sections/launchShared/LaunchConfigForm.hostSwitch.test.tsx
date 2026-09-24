import type { LaunchConfigLayer, LaunchConfigResolved, LaunchOptionSchemaResponse } from "@evener/appwire-client";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { Toast } from "../../../../widgets";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { LaunchConfigForm } from "./LaunchConfigForm";

// LaunchConfigForm's own contract across a host switch (component 07b, round
// 8). The form seeds its draft from `current` and submits it through `onSave`;
// a host-scoped parent hands it a different host - and with it a different
// `current` and a different `onSave` - while it stays mounted. A draft seeded
// once, keyed on nothing, would then be shown as the new host's settings and
// submitted to the NEW host by Save: the previous host's values written to the
// host the user just switched to.
//
// Pinned here at the component, where the seeded-once assumption lives. The
// frame above a real pane (hostScopedSurface.tsx) remounts the body on a
// switch as well, so this holds from both ends.

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

function renderForm(
  host: string,
  current: LaunchConfigLayer,
  onSave: (config: LaunchConfigLayer) => Promise<LaunchConfigResolved>,
) {
  return render(
    <>
      <Toast />
      <LaunchConfigForm
        options={SCHEMA.options}
        layer="global"
        current={current}
        successToast="Launch defaults saved"
        validatePath={async (path) => ({ valid: true, path })}
        host={host}
        onSave={onSave}
      />
    </>,
  );
}

afterEach(() => {
  cleanup();
  resetToastStoreForTests();
});

test("a host switch re-seeds the draft, so Save emits the NEW host's values", async () => {
  const onSave = vi.fn(async (_config: LaunchConfigLayer) => RESOLVED);
  const { rerender } = renderForm("alpha", { agent: "alpha-agent" }, onSave);
  const user = userEvent.setup();
  const agent = screen.getByLabelText("Agent") as HTMLInputElement;
  expect(agent.value).toBe("alpha-agent");

  // The user edits alpha's settings and does NOT save them yet.
  await user.clear(agent);
  await user.type(agent, "typed-for-alpha");
  expect(agent.value).toBe("typed-for-alpha");

  // The parent switches hosts without unmounting the form.
  rerender(
    <>
      <Toast />
      <LaunchConfigForm
        options={SCHEMA.options}
        layer="global"
        current={{ agent: "beta-agent" }}
        successToast="Launch defaults saved"
        validatePath={async (path) => ({ valid: true, path })}
        host="beta"
        onSave={onSave}
      />
    </>,
  );

  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("beta-agent");

  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  expect(onSave).toHaveBeenCalledTimes(1);
  expect(onSave.mock.calls[0]?.[0]).toEqual({ agent: "beta-agent" });
});

test("a re-render for the SAME host keeps the user's unsaved edits", async () => {
  const onSave = vi.fn(async (_config: LaunchConfigLayer) => RESOLVED);
  const { rerender } = renderForm("alpha", { agent: "alpha-agent" }, onSave);
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("Agent"), "-edited");

  // A same-host re-render - the pane re-fetching, a new `current` object for
  // the same host - must not throw away what is being typed.
  rerender(
    <>
      <Toast />
      <LaunchConfigForm
        options={SCHEMA.options}
        layer="global"
        current={{ agent: "alpha-agent" }}
        successToast="Launch defaults saved"
        validatePath={async (path) => ({ valid: true, path })}
        host="alpha"
        onSave={onSave}
      />
    </>,
  );

  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("alpha-agent-edited");
});

/** The form alone, WITHOUT <Toast/>: the toast widget runs a timer of its own
 * whose teardown would be counted under the same spy, and the toast store is a
 * module singleton, so a save's toast does not need the widget mounted. */
function renderFormOnly(
  host: string,
  current: LaunchConfigLayer,
  onSave: (config: LaunchConfigLayer) => Promise<LaunchConfigResolved>,
) {
  return render(
    <LaunchConfigForm
      options={SCHEMA.options}
      layer="global"
      current={current}
      successToast="Launch defaults saved"
      validatePath={async (path) => ({ valid: true, path })}
      host={host}
      onSave={onSave}
    />,
  );
}

// The status line's self-clear timer is the FORM's, and its teardown belongs to
// the form's lifecycle rather than to a render pass. A render-phase clearTimeout
// runs once per render React makes - including a pass StrictMode double-invokes
// and a pass React discards under concurrent rendering - so it can cancel a
// timer for a reseed no commit ever carried out. And it can never run when the
// form goes away at all: unmounting runs no render, so a timer armed by a save
// is simply dropped. The teardown is an effect's, which is the one place that
// runs on both.
test("the pending status timer is torn down when the form goes away", async () => {
  const onSave = vi.fn(async (_config: LaunchConfigLayer) => RESOLVED);
  const { unmount } = renderFormOnly("alpha", { agent: "alpha-agent" }, onSave);
  const user = userEvent.setup();
  // Saving arms the self-clear timer (STATUS_CLEAR_MS), so a teardown is
  // pending when the form goes away.
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  const clear = vi.spyOn(globalThis, "clearTimeout");
  unmount();
  expect(clear).toHaveBeenCalledTimes(1);
  clear.mockRestore();
});

// The same teardown on the other lifecycle edge it belongs to: a host switch
// (a re-registration included) drops the status line, so its timer is cancelled
// once - not once per render pass the reseed is reached from.
test("a host switch tears the status timer down exactly once", async () => {
  const onSave = vi.fn(async (_config: LaunchConfigLayer) => RESOLVED);
  const { rerender } = renderFormOnly("alpha", { agent: "alpha-agent" }, onSave);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));

  const clear = vi.spyOn(globalThis, "clearTimeout");
  rerender(
    <LaunchConfigForm
      options={SCHEMA.options}
      layer="global"
      current={{ agent: "beta-agent" }}
      successToast="Launch defaults saved"
      validatePath={async (path) => ({ valid: true, path })}
      host="beta"
      onSave={onSave}
    />,
  );
  expect(clear).toHaveBeenCalledTimes(1);
  clear.mockRestore();
});

/** The form with the per-host store instance its parent resolved. */
function renderOwnedForm(
  host: string,
  current: LaunchConfigLayer,
  draftOwner: object,
  onSave: (config: LaunchConfigLayer) => Promise<LaunchConfigResolved>,
) {
  return render(
    <LaunchConfigForm
      options={SCHEMA.options}
      layer="global"
      current={current}
      successToast="Launch defaults saved"
      validatePath={async (path) => ({ valid: true, path })}
      host={host}
      draftOwner={draftOwner}
      onSave={onSave}
    />,
  );
}

// The draft belongs to the per-host STORE INSTANCE, not to the host's name. A
// host removed and re-added under the same name is a different registration -
// stores/launchConfig.ts's hostEntry builds a NEW instance for it, with a new
// `current`, a new `onSave` and new `paths` - while the name is unchanged. Keyed
// on the name, a draft typed for the old registration survives the swap and Save
// writes it to the NEW one.
test("a re-registration under the same name re-seeds the draft, so Save emits the NEW registration's values", async () => {
  const onSave = vi.fn(async (_config: LaunchConfigLayer) => RESOLVED);
  const firstRegistration = { instance: "first" };
  const secondRegistration = { instance: "second" };
  const { rerender } = renderOwnedForm("beta", { agent: "beta-agent" }, firstRegistration, onSave);
  const user = userEvent.setup();
  const agent = screen.getByLabelText("Agent") as HTMLInputElement;
  expect(agent.value).toBe("beta-agent");
  await user.clear(agent);
  await user.type(agent, "typed-for-the-old-registration");
  expect(agent.value).toBe("typed-for-the-old-registration");

  // Removed and re-added under the same name: a different instance, so a
  // different registration to write to.
  rerender(
    <LaunchConfigForm
      options={SCHEMA.options}
      layer="global"
      current={{ agent: "beta-agent-new" }}
      successToast="Launch defaults saved"
      validatePath={async (path) => ({ valid: true, path })}
      host="beta"
      draftOwner={secondRegistration}
      onSave={onSave}
    />,
  );

  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("beta-agent-new");
  await user.click(screen.getByRole("button", { name: "Save launch defaults" }));
  expect(onSave).toHaveBeenCalledTimes(1);
  expect(onSave.mock.calls[0]?.[0]).toEqual({ agent: "beta-agent-new" });
});

// The other half of the same rule, and the reason the instance - not a revision
// or a refetch counter - is the owner: this host's pane re-reading its layer
// hands the form a referentially new `current` while the instance is the same,
// and that must not throw away what is being typed.
test("a re-read for the SAME instance keeps the user's unsaved edits", async () => {
  const onSave = vi.fn(async (_config: LaunchConfigLayer) => RESOLVED);
  const registration = { instance: "first" };
  const { rerender } = renderOwnedForm("beta", { agent: "beta-agent" }, registration, onSave);
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("Agent"), "-edited");

  rerender(
    <LaunchConfigForm
      options={SCHEMA.options}
      layer="global"
      current={{ agent: "beta-agent" }}
      successToast="Launch defaults saved"
      validatePath={async (path) => ({ valid: true, path })}
      host="beta"
      draftOwner={registration}
      onSave={onSave}
    />,
  );

  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("beta-agent-edited");
});
