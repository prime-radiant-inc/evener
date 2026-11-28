import type { AuthStatusResponse, InstanceListResponse } from "@evener/appwire-client";
import { ErrorEndpointConflict, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { connectionStore, resetConnectionStoreForTests } from "../../../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../../../stores/credentials";
import { setMutationClientIdentityForTests } from "../../../../stores/mutationClientIdentity";
import { connectReadyFakeClient } from "../../../../testing/connectionTestUtils";
import { enterText } from "../../../../textEntryTestUtils";
import { Toast } from "../../../../widgets";
import { resetToastStoreForTests } from "../../../../widgets/toast/store";
import { toastKinds } from "../../../../widgets/toast/toastTestUtils";
import { instance } from "./credentialsTestUtils";
import { ApiKeyDialog, CredentialJsonDialog } from "./instanceDialogs";

// The dialogs' outcomes, read from their markers: the inline refusal by its
// data-reason and the toasts by kind, never by the words either shows.
const alertReason = (): string | null => screen.getByRole("alert").getAttribute("data-reason");

beforeEach(() => {
  resetConnectionStoreForTests();
  resetCredentialsStoreForTests();
  // Each test reads the toasts its own actions raised.
  resetToastStoreForTests();
  // The auth mutations carry the page's identity on the wire now, so the
  // assertions that pin their exact params need one that cannot vary.
  setMutationClientIdentityForTests("test-tab");
});

afterEach(() => {
  resetConnectionStoreForTests();
  cleanup();
});

describe("ApiKeyDialog", () => {
  test("submitting an empty (trimmed) value silently cancels - no RPC", async () => {
    const fake = connectReadyFakeClient();
    const setKey = vi.fn();
    fake.on("evener/auth/apiKey/set", setKey);
    const onCancel = vi.fn();
    const user = userEvent.setup();
    render(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic" })}
        expectedEndpointFingerprint={undefined}
        onCancel={onCancel}
        onSuccess={() => {}}
      />,
    );
    await user.type(screen.getByLabelText(/api key/i, { selector: "input" }), "   ");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(setKey).not.toHaveBeenCalled();
    expect(onCancel).toHaveBeenCalled();
  });

  test("a non-empty key calls authApiKeySet, refreshes, toasts, and calls onSuccess", async () => {
    const fake = connectReadyFakeClient();
    fake.on("evener/auth/apiKey/set", (params) => {
      expect(params).toEqual({ provider: "work", value: "sk-secret", originClientId: "test-tab" });
      return { provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic" })}
          expectedEndpointFingerprint={undefined}
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSuccess).toHaveBeenCalled());
    expect(toastKinds()).toEqual(["success"]);
  });

  // The dialog is opened against one row's endpoint, and the value typed into
  // it belongs to that endpoint. A concurrent change can put a different
  // instance under the same name while the field holds the secret; submitting
  // then would send it to a destination the user never reviewed. The captured
  // fingerprint is what the write asserts, and a mismatch refuses the save
  // before any RPC, clears the field, and says why.
  test("a name that resolves to a different endpoint refuses the save, clears the value, and shows an error", async () => {
    const fake = connectReadyFakeClient();
    const setKey = vi.fn();
    fake.on("evener/auth/apiKey/set", setKey);
    const user = userEvent.setup();
    const { rerender } = render(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
        expectedEndpointFingerprint="fp-original"
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    // The listing for this name now carries a different endpoint.
    rerender(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-changed" })}
        expectedEndpointFingerprint="fp-original"
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(alertReason()).toBe("endpoint-changed"));
    expect(setKey).not.toHaveBeenCalled();
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");
  });

  // The positive control: while the name still resolves to the endpoint the
  // dialog opened against, the captured fingerprint is what the write asserts,
  // not a live value read at submit time.
  test("an unchanged destination submits the captured fingerprint", async () => {
    const fake = connectReadyFakeClient();
    fake.on("evener/auth/apiKey/set", (params) => {
      expect(params).toEqual({
        provider: "work",
        value: "sk-secret",
        expectedEndpointFingerprint: "fp-original",
        originClientId: "test-tab",
      });
      return { provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSuccess).toHaveBeenCalled());
  });

  // An undefined capture used to exempt the guard, so a row that gained a
  // fingerprint while the dialog sat open went out with no assertion. A hub
  // that keeps endpoint fingerprints refuses that write - it can describe the
  // destination, so an empty assertion leaves it unverified - which left the
  // user in a server refusal loop no local retry could break. The guard now
  // compares an undefined capture too: the row gaining one is a change to
  // refuse like any other, and the same local refusal re-anchors the
  // expectation to the row on screen so the retype saves against the
  // destination the user can see.
  test("a fingerprint gained while the dialog is open refuses the save and re-anchors to the row", async () => {
    const fake = connectReadyFakeClient();
    const setKey = vi.fn(() => ({
      provider: "work",
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    fake.on("evener/auth/apiKey/set", setKey);
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic" })}
        expectedEndpointFingerprint={undefined}
        onCancel={() => {}}
        onSuccess={onSuccess}
      />,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    // The listing entry this name resolves to gains an endpoint identity while
    // the field holds the secret.
    rerender(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-new" })}
        expectedEndpointFingerprint={undefined}
        onCancel={() => {}}
        onSuccess={onSuccess}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    // Refused locally, before any RPC: the typed value asserted no endpoint
    // while the row now carries one.
    expect(setKey).not.toHaveBeenCalled();
    await waitFor(() => expect(alertReason()).toBe("endpoint-changed"));
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");

    // The refusal re-anchored the expectation to the row on screen: the
    // re-typed value saves against the destination the dialog now displays.
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret-again");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(fake.calls.find((c) => c.method === "evener/auth/apiKey/set")?.params).toEqual({
        provider: "work",
        value: "sk-secret-again",
        expectedEndpointFingerprint: "fp-new",
        originClientId: "test-tab",
      }),
    );
    expect(onSuccess).toHaveBeenCalled();
  });

  // The hub serves a fingerprint only while it can key one, and the listing
  // omits what it cannot serve. A row that still carries a destination but no
  // fingerprint is therefore a destination the hub cannot describe right now -
  // the state a form opened during a key outage keeps when the listing has not
  // refreshed since. Submitting would assert nothing for an endpoint nobody
  // checked, so it is refused locally before any RPC, and the typed value is
  // kept for the retry the message asks for.
  test("a destination with no fingerprint refuses the save before any RPC", async () => {
    const fake = connectReadyFakeClient();
    const setKey = vi.fn();
    fake.on("evener/auth/apiKey/set", setKey);
    const user = userEvent.setup();
    render(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic", baseUrl: "https://work.example/v1" })}
        expectedEndpointFingerprint={undefined}
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(setKey).not.toHaveBeenCalled();
    await waitFor(() => expect(alertReason()).toBe("fingerprint-unavailable"));
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("sk-secret");
  });

  // The same state spelled as an explicit empty fingerprint, which a producer
  // that does not omit empty strings (or a fixture) carries; both spellings
  // mean "the row shows none" and both are refused.
  test("an explicit empty fingerprint on a destination refuses the save", async () => {
    const fake = connectReadyFakeClient();
    const setKey = vi.fn();
    fake.on("evener/auth/apiKey/set", setKey);
    const user = userEvent.setup();
    render(
      <ApiKeyDialog
        instance={instance({
          name: "work",
          providerId: "anthropic",
          baseUrl: "https://work.example/v1",
          endpointFingerprint: "",
        })}
        expectedEndpointFingerprint=""
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(setKey).not.toHaveBeenCalled();
    await waitFor(() => expect(alertReason()).toBe("fingerprint-unavailable"));
  });

  // The control: a row with no destination (baseUrl empty) and no fingerprint
  // is the "nothing shown, nothing to assert" case - the hub accepts a write
  // with no assertion there - so the dialog must still submit it.
  test("a row with no destination still submits with no assertion", async () => {
    const fake = connectReadyFakeClient();
    fake.on("evener/auth/apiKey/set", (params) => {
      expect(params).toEqual({ provider: "work", value: "sk-secret", originClientId: "test-tab" });
      return { provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", baseUrl: "" })}
          expectedEndpointFingerprint={undefined}
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSuccess).toHaveBeenCalled());
  });

  // The other direction of the same guard: a capture that was defined when the
  // dialog opened and whose endpoint has since disappeared is a change too, so
  // the save must refuse rather than assert a fingerprint the row no longer
  // carries.
  test("a captured fingerprint that the row no longer carries refuses the save", async () => {
    const fake = connectReadyFakeClient();
    const setKey = vi.fn();
    fake.on("evener/auth/apiKey/set", setKey);
    const user = userEvent.setup();
    const { rerender } = render(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
        expectedEndpointFingerprint="fp-original"
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    // The row loses its endpoint identity while the field holds the secret.
    rerender(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic" })}
        expectedEndpointFingerprint="fp-original"
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(alertReason()).toBe("endpoint-changed"));
    expect(setKey).not.toHaveBeenCalled();
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");
  });

  // The refusal tells the user to enter the value again, so re-entering it has
  // to be able to succeed. The row on screen is the destination the user can
  // review, so the refusal re-anchors the dialog's expectation to it: the
  // re-typed value asserts the row the dialog is displaying instead of being
  // refused forever against the endpoint that is already gone.
  test("a value re-entered after a refusal asserts the row the dialog now displays", async () => {
    const fake = connectReadyFakeClient();
    fake.on("evener/auth/apiKey/set", () => ({
      provider: "work",
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    // The listing for this name now carries a different endpoint.
    rerender(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-changed" })}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(alertReason()).toBe("endpoint-changed"));
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");

    // The error asks for the value again; entering it again saves against the
    // destination the dialog is now showing.
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret-again");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(fake.calls.find((c) => c.method === "evener/auth/apiKey/set")?.params).toEqual({
        provider: "work",
        value: "sk-secret-again",
        expectedEndpointFingerprint: "fp-changed",
        originClientId: "test-tab",
      }),
    );
    expect(onSuccess).toHaveBeenCalled();
  });

  // The guard above only sees a change the client's listing already reflects.
  // A move that lands between that listing and the write is invisible here -
  // the hub is the one that sees it, and it refuses the assertion
  // (appwire.Conflict: -32013 with evenerErrorInfo "conflict"). That refusal is
  // the same change, so it gets the same recovery the local guard makes: drop
  // the secret, re-read the listing, and re-anchor to the row now on screen, so
  // the retype the message asks for asserts the destination the user can
  // review. Reported as a generic save failure it left the dialog retrying a
  // dead assertion until something else refreshed.
  test("a hub endpoint-refusal drops the secret, re-reads the listing, and re-anchors to the row now on screen", async () => {
    const fake = connectReadyFakeClient();
    const MOVED = instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-changed" });
    fake.on("evener/instance/list", () => ({ instances: [MOVED], availableProviders: [] }));
    let attempts = 0;
    fake.on("evener/auth/apiKey/set", () => {
      attempts += 1;
      if (attempts === 1) {
        throw new WireError(
          "work no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again",
          -32013,
          { evenerErrorInfo: ErrorEndpointConflict },
        );
      }
      return { provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    // The toast queue is a module singleton shared across this file's tests;
    // clear it so "no save-failure toast" means this refusal pushed none.
    resetToastStoreForTests();
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));

    // The refusal is reported as the change it is, never as a save failure: the
    // hub's own words stay out of the alert and no "Save failed" toast goes up.
    await waitFor(() => expect(alertReason()).toBe("endpoint-changed"));
    expect(screen.getByRole("alert").textContent).not.toContain("no longer resolves");
    expect(toastKinds()).not.toContain("error");
    // The value belonged to the endpoint that is gone.
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");
    // The write that was refused asserted the endpoint the dialog opened on...
    expect(fake.calls.find((c) => c.method === "evener/auth/apiKey/set")?.params).toEqual({
      provider: "work",
      value: "sk-secret",
      expectedEndpointFingerprint: "fp-original",
      originClientId: "test-tab",
    });
    // ...and the dialog re-read the listing to find the row now on screen.
    expect(fake.calls.filter((call) => call.method === "evener/instance/list")).toHaveLength(1);
    // The parent renders this dialog's row from the listing, so the refreshed
    // row is what the dialog now shows; the next submit's guard compares
    // against the destination the re-anchor just adopted.
    rerender(
      <>
        <ApiKeyDialog
          instance={MOVED}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );

    // The re-typed value asserts that row's endpoint, so the retry the message
    // asks for saves against the destination the user can review instead of
    // being refused forever against the one that already moved.
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret-again");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(fake.calls.filter((c) => c.method === "evener/auth/apiKey/set")[1]?.params).toEqual({
        provider: "work",
        value: "sk-secret-again",
        expectedEndpointFingerprint: "fp-changed",
        originClientId: "test-tab",
      }),
    );
    expect(onSuccess).toHaveBeenCalled();
  });

  // The same refusal with nothing left to re-anchor to: the re-read no longer
  // shows a row for the name at all. There is no destination on screen to
  // assert, so the dialog says the connection changed and the value was not
  // saved - it neither keeps a retry that silently goes nowhere nor empties the
  // assertion, which would let the retype land on whatever the name resolves to
  // next, unverified.
  test("a hub endpoint-refusal whose row is gone reports the change and saves nothing", async () => {
    const fake = connectReadyFakeClient();
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    fake.on("evener/auth/apiKey/set", () => {
      throw new WireError(
        "work no longer resolves to the endpoint this form was opened on: review its destination and enter the credential again",
        -32013,
        { evenerErrorInfo: ErrorEndpointConflict },
      );
    });
    resetToastStoreForTests();
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(alertReason()).toBe("endpoint-vanished"));
    expect(screen.getByRole("alert").textContent).not.toContain("no longer resolves");
    expect(toastKinds()).not.toContain("error");
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");
    expect(fake.calls.filter((call) => call.method === "evener/instance/list")).toHaveLength(1);
    expect(onSuccess).not.toHaveBeenCalled();

    // A retry while the row is still gone is answered by the same honest
    // message, and it still asserts the endpoint the user last reviewed rather
    // than saving unverified.
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret-again");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(alertReason()).toBe("endpoint-vanished"));
    expect(fake.calls.filter((c) => c.method === "evener/auth/apiKey/set")[1]?.params).toEqual({
      provider: "work",
      value: "sk-secret-again",
      expectedEndpointFingerprint: "fp-original",
      originClientId: "test-tab",
    });
    expect(onSuccess).not.toHaveBeenCalled();
  });

  // The store's own refusal of a write from the previous connection's listing
  // (stores/credentials.ts's requireWritableClient) is the same change the
  // hub's conflict reports: the endpoint fingerprint this dialog captured, and
  // the value typed against it, belong to a connection that is gone. It gets
  // the same recovery, and the user never sees the store's internal words or a
  // "Save failed" toast for a save that was deliberately not sent.
  test("a save from the previous connection's listing is refused, re-reads, and re-anchors to the row now on screen", async () => {
    const fake = connectReadyFakeClient();
    const MOVED = instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-changed" });
    fake.on("evener/instance/list", () => ({ instances: [MOVED], availableProviders: [] }));
    await credentialsStore.getState().fetch();

    // A replacement whose own listing read is held open: everything on screen
    // still names the connection that is gone. Once released it stays resolved,
    // so the reads the recovery and the retry wait on see one listing.
    let finishRestore!: (value: InstanceListResponse) => void;
    const restore = new Promise<InstanceListResponse>((resolve) => {
      finishRestore = resolve;
    });
    const replacement = new FakeClient("ready");
    replacement.on("evener/instance/list", () => restore);
    replacement.on("evener/auth/apiKey/set", () => ({
      provider: "work",
      supported: true,
      signedIn: true,
      activeSource: "store",
      hasStoredOAuth: false,
    }));
    connectionStore.getState().connect(replacement);
    expect(credentialsStore.getState().listingFromPreviousConnection).toBe(true);

    resetToastStoreForTests();
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic", endpointFingerprint: "fp-original" })}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));

    // The write was refused before any RPC reached the replacement connection.
    expect(replacement.calls.filter((call) => call.method === "evener/auth/apiKey/set")).toHaveLength(0);
    // The recovery waits on this connection's own listing read; answer it with
    // the row the re-anchor adopts.
    await waitFor(() => expect(replacement.calls.some((call) => call.method === "evener/instance/list")).toBe(true));
    await act(async () => finishRestore({ instances: [MOVED], availableProviders: [] }));
    await waitFor(() => expect(alertReason()).toBe("connection-replaced"));
    expect(toastKinds()).not.toContain("error");
    // The value was typed for the listing that is gone.
    expect((screen.getByLabelText(/api key/i, { selector: "input" }) as HTMLInputElement).value).toBe("");

    // The parent renders this dialog's row from the listing, so the refreshed
    // row is what the dialog now shows.
    rerender(
      <>
        <ApiKeyDialog
          instance={MOVED}
          expectedEndpointFingerprint="fp-original"
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );

    // The re-typed value asserts the endpoint the re-anchor adopted, so the
    // retry the message asks for saves against the destination the user can
    // review instead of being refused against one that is gone.
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret-again");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(replacement.calls.filter((call) => call.method === "evener/auth/apiKey/set")[0]?.params).toEqual({
        provider: "work",
        value: "sk-secret-again",
        expectedEndpointFingerprint: "fp-changed",
        originClientId: "test-tab",
      }),
    );
    expect(onSuccess).toHaveBeenCalled();
  });

  test("a saved key whose listing read is lost is still reported as saved", async () => {
    const fake = connectReadyFakeClient();
    let save!: (value: AuthStatusResponse) => void;
    fake.on(
      "evener/auth/apiKey/set",
      () =>
        new Promise<AuthStatusResponse>((resolve) => {
          save = resolve;
        }),
    );
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic" })}
          expectedEndpointFingerprint={undefined}
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await enterText(user, screen.getByLabelText(/api key/i, { selector: "input" }), "sk-secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    // Dropped connection during the save: the credential write succeeded, and
    // the follow-up listing read rejecting must not turn it into "Save failed".
    resetConnectionStoreForTests();
    await act(async () =>
      save({ provider: "work", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false }),
    );
    expect(onSuccess).toHaveBeenCalled();
    expect(within(screen.getByRole("dialog")).queryByRole("alert")).toBeNull();
  });

  test("a failure shows an inline error and a 'Save failed' toast", async () => {
    const fake = connectReadyFakeClient();
    fake.on("evener/auth/apiKey/set", () => {
      throw new Error("rejected");
    });
    const user = userEvent.setup();
    render(
      <>
        <ApiKeyDialog
          instance={instance({ name: "work", providerId: "anthropic" })}
          expectedEndpointFingerprint={undefined}
          onCancel={() => {}}
          onSuccess={() => {}}
        />
        <Toast />
      </>,
    );
    await user.type(screen.getByLabelText(/api key/i, { selector: "input" }), "sk-bad");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(alertReason()).toBe("save-failed"));
    expect(screen.getByRole("alert").textContent).toBe("rejected");
    expect(toastKinds()).toEqual(["error"]);
  });

  test("the API key input is type=password", () => {
    connectReadyFakeClient();
    render(
      <ApiKeyDialog
        instance={instance({ name: "work", providerId: "anthropic" })}
        expectedEndpointFingerprint={undefined}
        onCancel={() => {}}
        onSuccess={() => {}}
      />,
    );
    expect(screen.getByLabelText(/api key/i, { selector: "input" }).getAttribute("type")).toBe("password");
  });
});

describe("CredentialJsonDialog", () => {
  test("submitting an empty (trimmed) value silently cancels - no RPC", async () => {
    const fake = connectReadyFakeClient();
    const setJson = vi.fn();
    fake.on("evener/auth/credentialJson/set", setJson);
    const onCancel = vi.fn();
    const user = userEvent.setup();
    render(
      <CredentialJsonDialog
        instance={instance({ name: "vertex", providerId: "google-vertex", auth: "gcp-adc" })}
        expectedEndpointFingerprint={undefined}
        onCancel={onCancel}
        onSuccess={() => {}}
      />,
    );
    await user.type(screen.getByLabelText(/credential json/i, { selector: "textarea" }), "   ");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(setJson).not.toHaveBeenCalled();
    expect(onCancel).toHaveBeenCalled();
  });

  test("a pasted JSON calls credentialJson/set with the instance name, refreshes, toasts, and calls onSuccess", async () => {
    const fake = connectReadyFakeClient();
    const json = '{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}';
    fake.on("evener/auth/credentialJson/set", (params) => {
      expect(params).toEqual({ provider: "vertex", value: json, originClientId: "test-tab" });
      return { provider: "vertex", supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <CredentialJsonDialog
          instance={instance({ name: "vertex", providerId: "google-vertex", auth: "gcp-adc" })}
          expectedEndpointFingerprint={undefined}
          onCancel={() => {}}
          onSuccess={onSuccess}
        />
        <Toast />
      </>,
    );
    await user.click(screen.getByLabelText(/credential json/i, { selector: "textarea" }));
    await user.paste(json);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSuccess).toHaveBeenCalled());
    expect(toastKinds()).toEqual(["success"]);
  });

  test("a rejected paste shows the server's message inline", async () => {
    const fake = connectReadyFakeClient();
    fake.on("evener/auth/credentialJson/set", () => {
      throw new Error("not a Google credential JSON: unexpected end of JSON input");
    });
    const user = userEvent.setup();
    render(
      <>
        <CredentialJsonDialog
          instance={instance({ name: "vertex", providerId: "google-vertex", auth: "gcp-adc" })}
          expectedEndpointFingerprint={undefined}
          onCancel={() => {}}
          onSuccess={() => {}}
        />
        <Toast />
      </>,
    );
    await user.click(screen.getByLabelText(/credential json/i, { selector: "textarea" }));
    await user.paste("{");
    await user.click(screen.getByRole("button", { name: "Save" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("not a Google credential JSON");
  });
});
