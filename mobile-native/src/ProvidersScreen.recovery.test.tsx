// Screen-level tests for the two async paths `useProviderSurface` cannot cover:
// which provider a late probe error names, whether a superseded instance write
// is reported as success, and whether a save with no fingerprint is refused.
// Every native edge the screen reaches is mocked here, exactly as
// ProvidersScreen.test.tsx does; the store is driven through a scripted client.
import type { ComponentProps } from "react";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";
import type { AnyNotification } from "@evener/appwire-client";
import {
  ENDPOINT_CHANGED_TEST_MESSAGE,
  ErrorEndpointConflict,
  FINGERPRINT_UNAVAILABLE_TEST_MESSAGE,
  WireError,
} from "@evener/appwire-client";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { ProvidersScreen } from "./ProvidersScreen";
import { render, renderedText, screenConnection } from "./renderNative.testkit";

const harness = vi.hoisted(() => ({
  connection: {} as Record<string, unknown>,
}));
const alerts = vi.hoisted(() => ({ alert: vi.fn() }));
vi.mock("react-native", async () => {
  const mock = (await import("./renderNative.testkit")).nativeModuleMock();
  return { ...mock, Alert: { alert: alerts.alert } };
});
vi.mock("react-native-safe-area-context", () => ({ SafeAreaView: "SafeAreaView" }));
vi.mock("expo-crypto", () => ({ randomUUID: () => "fixture-uuid" }));
vi.mock("expo-clipboard", () => ({ setStringAsync: async () => {} }));
vi.mock("./ConnectionProvider", () => ({ useConnection: () => harness.connection }));

afterEach(() => {
  vi.useRealTimers();
});

// alpha and beta are two rows of one hub; each field the screen reads is present.
const row = (
  name: string,
  extra: Record<string, unknown> = {},
): Record<string, unknown> => ({
  name,
  providerId: "anthropic",
  protocol: "https",
  auth: "apiKey",
  implicit: false,
  isDefault: true,
  activeSource: "store",
  hasStoredOAuth: false,
  credentialRequired: true,
  ...extra,
});

function rows(...instances: Record<string, unknown>[]): {
  instances: Record<string, unknown>[];
  availableProviders: never[];
  diagnostics: string[];
} {
  return { instances, availableProviders: [], diagnostics: ["from the hub"] };
}

// A client that records methods, answers from `io`, and hands tests the
// notification feed the store listens on.
function hub(io: { request: (method: string, params: unknown) => Promise<unknown> }) {
  const methods: string[] = [];
  const handlers = new Set<(n: AnyNotification) => void>();
  const client = {
    request: (method: string, params: unknown) => {
      methods.push(method);
      return io.request(method, params);
    },
    onNotification: (handler: (n: AnyNotification) => void) => {
      handlers.add(handler);
      return () => {
        handlers.delete(handler);
      };
    },
  } as ConversationClientLike;
  return {
    client,
    methods,
    notify: (notification: AnyNotification) => {
      for (const handler of handlers) handler(notification);
    },
  };
}

function mount(io: { request: (method: string, params: unknown) => Promise<unknown> }) {
  const scripted = hub(io);
  harness.connection = screenConnection(scripted.client, "ready");
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof ProvidersScreen>;
  const tree = render(<ProvidersScreen {...props} />);
  return { tree, scripted };
}

function pressables(tree: ReactTestRenderer) {
  return tree.root.findAll((node) => String(node.type) === "Pressable");
}
function pressLabel(tree: ReactTestRenderer, label: string) {
  const target = pressables(tree).find(
    (node) => node.props.accessibilityLabel === label,
  );
  if (!target) throw new Error(`no pressable labelled ${label}`);
  act(() => target.props.onPress());
}
function pressRow(tree: ReactTestRenderer, name: string) {
  const target = pressables(tree).find(
    (node) =>
      typeof node.props.accessibilityLabel === "string" &&
      node.props.accessibilityLabel.startsWith(`${name}`),
  );
  if (!target) throw new Error(`no row for ${name}`);
  act(() => target.props.onPress());
}

it("does not surface a late probe error on the provider selected after it", async () => {
  const probe = deferred<unknown>();
  const { tree } = mount({
    request: (method: string) =>
      method === "evener/auth/test"
        ? probe.promise
        : Promise.resolve(rows(row("alpha"), row("beta"))),
  });
  await act(async () => {});
  pressRow(tree, "alpha");
  pressLabel(tree, "Test credentials");
  // The user moves to another provider while alpha's probe is in flight.
  pressRow(tree, "beta");
  probe.reject(new WireError("conflict", -32013, { evenerErrorInfo: "conflict" }));
  await act(async () => {});
  expect(renderedText(tree)).not.toContain(ENDPOINT_CHANGED_TEST_MESSAGE);
});

it("reports a superseded instance write as unconfirmed, not as success", async () => {
  vi.useFakeTimers();
  const write = deferred<unknown>();
  const beta = row("alpha", { isDefault: false });
  const listing = rows(beta);
  const { tree, scripted } = mount({
    request: (method: string) =>
      method === "evener/instance/setDefault"
        ? write.promise
        : Promise.resolve(listing),
  });
  await act(async () => {});
  pressRow(tree, "alpha");
  pressLabel(tree, "Make default");
  // A foreign auth change refetches: its read starts after the write and
  // supersedes the write's answer.
  scripted.notify({
    method: "evener/auth/updated",
    params: { provider: "alpha", activeSource: "oauth" },
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  write.resolve(listing);
  await act(async () => {});
  expect(renderedText(tree)).toContain("could not be confirmed");
});

it("refuses a credential save for a destination the hub cannot fingerprint", async () => {
  const rowWithoutFingerprint = row("alpha", {
    baseUrl: "https://alpha.example",
    authModes: ["apiKey"],
    hasStoredFile: false,
  });
  const listing = rows(rowWithoutFingerprint);
  const { tree, scripted } = mount({
    request: async () => listing,
  });
  await act(async () => {});
  pressRow(tree, "alpha");
  pressLabel(tree, "Set key");
  const input = tree.root
    .findAll((node) => String(node.type) === "TextInput")
    .find((node) => node.props.accessibilityLabel === "API key");
  if (!input) throw new Error("no API key input");
  act(() => input.props.onChangeText("fixture-key"));
  pressLabel(tree, "Save key");
  await act(async () => {});
  expect(scripted.methods).not.toContain("evener/auth/apiKey/set");
  expect(renderedText(tree)).toContain("credential was not saved");
});

it("refuses a credential-JSON save for a destination the hub cannot fingerprint", async () => {
  const listing = rows(
    row("alpha", {
      baseUrl: "https://alpha.example",
      authModes: ["credentialJson"],
      hasStoredFile: false,
    }),
  );
  const { tree, scripted } = mount({ request: async () => listing });
  await act(async () => {});
  pressRow(tree, "alpha");
  pressLabel(tree, "Set credential JSON");
  const input = tree.root
    .findAll((node) => String(node.type) === "TextInput")
    .find((node) => node.props.accessibilityLabel === "Google credential JSON");
  if (!input) throw new Error("no credential JSON input");
  act(() => input.props.onChangeText("{}"));
  pressLabel(tree, "Save credential JSON");
  await act(async () => {});
  expect(renderedText(tree)).toContain("credential was not saved");
  expect(scripted.methods).not.toContain("evener/auth/credentialJson/set");
});

it("does not report a destination change for a conflict on a non-asserting write", async () => {
  const listing = rows(row("alpha", { isDefault: false }));
  const { tree } = mount({
    request: async (method: string) => {
      if (method === "evener/instance/setDefault")
        throw new WireError("conflict", -32013, {
          evenerErrorInfo: "conflict",
        });
      return listing;
    },
  });
  await act(async () => {});
  pressRow(tree, "alpha");
  pressLabel(tree, "Make default");
  await act(async () => {});
  expect(renderedText(tree)).not.toContain("changed to a different endpoint");
  expect(renderedText(tree)).toContain("could not be confirmed");
});

it("refuses endpoint-sensitive destructive actions without a fingerprint, with a reason", async () => {
  for (const label of [
    "Clear stored key",
    "Clear credentials",
    "Remove instance",
  ]) {
    alerts.alert.mockClear();
    const listing = rows(
      row("alpha", {
        baseUrl: "https://alpha.example",
        hasStoredFile: true,
        activeSource: "oauth",
        authModes: ["oauth"],
      }),
    );
    const { tree, scripted } = mount({ request: async () => listing });
    await act(async () => {});
    pressRow(tree, "alpha");
    // Not silently greyed out: pressing says why it cannot proceed.
    pressLabel(tree, label);
    await act(async () => {});
    expect(renderedText(tree)).toContain("action was not run");
    // The guard returns before the confirmation, so no write can go out.
    expect(alerts.alert).not.toHaveBeenCalled();
    expect(scripted.methods).not.toContain("evener/instance/remove");
    expect(scripted.methods).not.toContain("evener/auth/logout");
    expect(scripted.methods).not.toContain("evener/auth/apiKey/clear");
  }
});

it("refuses a probe for a row the hub cannot fingerprint", async () => {
  const listing = rows(
    row("alpha", {
      baseUrl: "https://alpha.example",
      authModes: ["apiKey"],
      hasStoredFile: false,
    }),
  );
  const { tree, scripted } = mount({ request: async () => listing });
  await act(async () => {});
  pressRow(tree, "alpha");
  pressLabel(tree, "Test credentials");
  await act(async () => {});
  expect(renderedText(tree)).toContain(FINGERPRINT_UNAVAILABLE_TEST_MESSAGE);
  expect(scripted.methods).not.toContain("evener/auth/test");
});

it("clears the credential editor when another instance is selected", async () => {
  const listing = rows(
    row("alpha", {
      baseUrl: "https://alpha.example",
      endpointFingerprint: "fp-1",
      authModes: ["apiKey"],
      hasStoredFile: false,
    }),
    row("beta", {
      baseUrl: "https://beta.example",
      endpointFingerprint: "fp-2",
      authModes: ["apiKey"],
      hasStoredFile: false,
    }),
  );
  const { tree, scripted } = mount({ request: async () => listing });
  await act(async () => {});
  pressRow(tree, "alpha");
  pressLabel(tree, "Set key");
  // The alpha draft must not carry over to beta.
  pressRow(tree, "beta");
  await act(async () => {});
  expect(
    pressables(tree).find(
      (node) => node.props.accessibilityLabel === "Save key",
    ),
  ).toBeUndefined();
  expect(scripted.methods).not.toContain("evener/auth/apiKey/set");
});

it("leaves only the store's own reconcile read after an in-flight write outlives the screen", async () => {
  vi.useFakeTimers();
  const write = deferred<unknown>();
  const alpha = row("alpha", { isDefault: false });
  const listing = rows(alpha);
  const first = hub({
    request: (method) =>
      method === "evener/instance/setDefault"
        ? write.promise
        : Promise.resolve(listing),
  });
  const second = hub({ request: async () => listing });
  harness.connection = screenConnection(first.client, "ready");
  const props = {
    route: { params: { hubId: "hub-1" } },
  } as unknown as ComponentProps<typeof ProvidersScreen>;
  const tree = render(<ProvidersScreen {...props} />);
  await act(async () => {});
  pressRow(tree, "alpha");
  pressLabel(tree, "Make default");
  // The connection drops while the write is out: the list is torn down, then a
  // replacement connection remounts it.
  harness.connection = { ...harness.connection, client: null, state: "closed" };
  await act(async () => {
    tree.update(<ProvidersScreen {...props} />);
  });
  harness.connection = { ...harness.connection, client: second.client, state: "ready" };
  await act(async () => {
    tree.update(<ProvidersScreen {...props} />);
  });
  await act(async () => {});
  const reads = second.methods.filter(
    (method) => method === "evener/instance/list",
  ).length;
  // The old write's superseded answer drives exactly one read on the new
  // connection: the store's own setDefault reconcile (debounced 250 ms). The
  // unmounted screen's continuation must add none of its own.
  write.resolve(listing);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  expect(
    second.methods.filter((method) => method === "evener/instance/list").length,
  ).toBe(reads + 1);
});

it("clears a stale probe error when a new probe starts", async () => {
  const listing = rows(
    row("alpha", {
      baseUrl: "https://alpha.example",
      endpointFingerprint: "fp-alpha",
    }),
  );
  let probes = 0;
  const { tree } = mount({
    request: async (method: string) => {
      if (method === "evener/auth/test") {
        probes += 1;
        if (probes === 1)
          throw new WireError("conflict", -32013, {
            evenerErrorInfo: ErrorEndpointConflict,
          });
        return { provider: "alpha", status: "success", message: "" };
      }
      return listing;
    },
  });
  await act(async () => {});
  pressRow(tree, "alpha");
  pressLabel(tree, "Test credentials");
  await act(async () => {});
  expect(renderedText(tree)).toContain(ENDPOINT_CHANGED_TEST_MESSAGE);
  pressLabel(tree, "Test credentials");
  await act(async () => {});
  expect(renderedText(tree)).toContain("Credentials verified.");
  expect(renderedText(tree)).not.toContain(ENDPOINT_CHANGED_TEST_MESSAGE);
});

it("re-anchors the credential editor when the endpoint moves under it", async () => {
  vi.useFakeTimers();
  const before = rows(
    row("alpha", {
      baseUrl: "https://alpha.example",
      endpointFingerprint: "fp-1",
      authModes: ["apiKey"],
      hasStoredFile: false,
    }),
  );
  const after = rows(
    row("alpha", {
      baseUrl: "https://alpha.example",
      endpointFingerprint: "fp-2",
      authModes: ["apiKey"],
      hasStoredFile: false,
    }),
  );
  let lists = 0;
  const { tree, scripted } = mount({
    request: async (method: string) => {
      if (method === "evener/instance/list") {
        lists += 1;
        return lists === 1 ? before : after;
      }
      return after;
    },
  });
  await act(async () => {});
  pressRow(tree, "alpha");
  pressLabel(tree, "Set key");
  scripted.notify({
    method: "evener/auth/updated",
    params: { provider: "alpha", activeSource: "oauth" },
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  expect(
    pressables(tree).find(
      (node) => node.props.accessibilityLabel === "Save key",
    ),
  ).toBeUndefined();
  expect(scripted.methods).not.toContain("evener/auth/apiKey/set");
});

it("names a changed endpoint and re-reads when a credential save is refused", async () => {
  const listing = rows(
    row("alpha", {
      baseUrl: "https://alpha.example",
      endpointFingerprint: "fp-1",
      authModes: ["apiKey"],
      hasStoredFile: false,
    }),
  );
  let lists = 0;
  const { tree } = mount({
    request: async (method: string) => {
      if (method === "evener/instance/list") {
        lists += 1;
        return listing;
      }
      if (method === "evener/auth/apiKey/set")
        throw new WireError("conflict", -32013, {
          evenerErrorInfo: ErrorEndpointConflict,
        });
      return listing;
    },
  });
  await act(async () => {});
  pressRow(tree, "alpha");
  pressLabel(tree, "Set key");
  const input = tree.root
    .findAll((node) => String(node.type) === "TextInput")
    .find((node) => node.props.accessibilityLabel === "API key");
  if (!input) throw new Error("no API key input");
  act(() => input.props.onChangeText("fixture-key"));
  const listsBefore = lists;
  pressLabel(tree, "Save key");
  await act(async () => {});
  expect(renderedText(tree)).toContain("changed to a different endpoint");
  expect(lists).toBeGreaterThan(listsBefore);
});

it("recovers a superseded write with exactly one listing read", async () => {
  vi.useFakeTimers();
  const write = deferred<unknown>();
  const listing = rows(row("alpha", { isDefault: false }));
  const { tree, scripted } = mount({
    request: (method: string) =>
      method === "evener/instance/setDefault"
        ? write.promise
        : Promise.resolve(listing),
  });
  await act(async () => {});
  pressRow(tree, "alpha");
  pressLabel(tree, "Make default");
  // A foreign change refetches and supersedes the write's answer.
  scripted.notify({
    method: "evener/auth/updated",
    params: { provider: "alpha", activeSource: "oauth" },
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  const reads = scripted.methods.filter(
    (method) => method === "evener/instance/list",
  ).length;
  write.resolve(listing);
  await act(async () => {});
  await act(async () => {
    await vi.advanceTimersByTimeAsync(300);
  });
  // Exactly one recovery read: the store's own setDefault reconcile.
  expect(
    scripted.methods.filter((method) => method === "evener/instance/list")
      .length,
  ).toBe(reads + 1);
});

// deferred hands a test the resolver of a promise it scripts into a fake.
interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason: unknown) => void;
}
function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((complete, fail) => {
    resolve = complete;
    reject = fail;
  });
  return { promise, resolve, reject };
}
