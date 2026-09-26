// Screen-level tests for the removal paths the model tests cannot reach: how
// the browser reports a rejection the hub says already stood (never the
// generic failed write), when it refetches the list, and what a clone-litter
// rejection leaves on screen. The browser reports an applied outcome through
// the guard slot PluginsScreen wires around it in production, so the mount
// carries that wiring too. Mirrors ProvidersScreen.recovery.test.tsx's
// mocking: every native edge the screen reaches is mocked here, and the
// stores are driven through the SDK's FakeClient. The install- and
// removal-path tests beside them pin the write-gate posture every browser
// mutation shares: a press the gate refuses on readiness runs nothing and
// retires nothing.
import { useMemo, useRef, useState } from "react";
import { act, type ReactTestRenderer } from "react-test-renderer";
import { beforeEach, expect, it, vi } from "vitest";
import type { MarketplaceEntry } from "@evener/appwire-client";
import {
  ErrorMarketplaceRemoveApplied,
  WireError,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import {
  createMarketplacesStore,
  createPluginsStore,
} from "@evener/appwire-client/state/extensions";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { MarketplaceBrowser } from "./MarketplaceBrowser";
import { createPluginMutationGate } from "./pluginMutationGate";
import { ErrorMessage } from "./ui";
import {
  alertRequests,
  nativeModuleMock,
  render,
  renderedText,
} from "./renderNative.testkit";

vi.mock("react-native", async () => ({
  ...(await import("./renderNative.testkit")).nativeModuleMock(),
}));
vi.mock("react-native-safe-area-context", () => ({
  SafeAreaView: "SafeAreaView",
}));
// ConnectionStatus, embedded in the browser's sheets, imports the connection
// provider; this suite renders only the ready state, so the banner never
// calls the hook - but the module must load without the native expo graph
// the provider pulls in.
vi.mock("./ConnectionProvider", () => ({
  useConnection: () => ({
    state: "ready",
    error: null,
    retry: () => {},
    activeProfile: null,
  }),
}));

const ACME: MarketplaceEntry = {
  name: "acme",
  source: { kind: "github", repo: "acme/plugins" },
  lastUpdated: 1,
};

const NO_FENCES: ReadonlySet<string> = new Set();

beforeEach(() => {
  alertRequests.length = 0;
});

/** The guard PluginsScreen wires around the browser, in the minimal shape
 * these browser-level tests need: the names the browser reports applied
 * (fenced from removing again) held with the publication baseline each fence
 * predates, the warning slot the applied outcome's notice renders in, and a
 * client that never gets replaced. The guard's own client scoping and remount
 * survival is PluginsScreen.test.tsx's to pin. */
function GuardedBrowser({
  client,
  canUseConnection = () => true,
  fenced = NO_FENCES,
}: {
  client: ConversationClientLike;
  canUseConnection?: () => boolean;
  /** Names the screen has fenced since the last render: the browser's
   * appliedRemovalNames prop can change while a confirmation dialog is open,
   * the way the production screen's guard can. */
  fenced?: ReadonlySet<string>;
}) {
  const [guard, setGuard] = useState<ReadonlyMap<string, number>>(
    () => new Map(),
  );
  const [warning, setWarning] = useState<string | null>(null);
  // The screen's half of the store wiring, in this harness's minimal shape:
  // the marketplaces store outlives the browser, and the add answer is
  // captured beside it. Nothing here drives connection transitions - these
  // tests hold a ready connection throughout.
  const lastAddMarketplaces = useRef<readonly MarketplaceEntry[] | null>(null);
  const marketplaces = useMemo(() => createMarketplacesStore(client), [client]);
  const names = useMemo(
    () => new Set([...guard.keys(), ...fenced]),
    [guard, fenced],
  );
  return (
    <>
      <ErrorMessage message={warning} />
      <MarketplaceBrowser
        client={client}
        connectionState="ready"
        hubName="Work hub"
        installed={createPluginsStore(client)}
        marketplaces={marketplaces}
        lastAddMarketplaces={lastAddMarketplaces}
        gate={createPluginMutationGate()}
        ready={true}
        canUseConnection={canUseConnection}
        onOpenPlugin={() => {}}
        appliedRemovalNames={names}
        onAppliedRemoval={(name, notice, _owner, marketplaces, publicationVersion) => {
          // The production screen's recording rule, in this harness's
          // minimal shape: an accepted snapshot that already omits the
          // target is the outcome's own reconciliation and leaves no fence,
          // and a fence records the publication baseline the outcome read.
          setGuard((current) => {
            const next = new Map(current);
            if (marketplaces === null || marketplaces.some((item) => item.name === name))
              next.set(name, publicationVersion);
            else next.delete(name);
            return next;
          });
          if (notice !== null) setWarning(notice);
          return true;
        }}
        onAuthoritativeMarketplaces={(_marketplaces, _owner, publicationVersion) => {
          // The screen's fallback ruling, in this harness's minimal shape:
          // a fence retires with the first publication NEWER than its own
          // baseline, whatever the publication carries - per name, the way
          // the production screen prunes. A report that outruns no baseline
          // changes nothing, so the re-render a report triggers cannot loop
          // back into the reporting effect and a re-report of an unchanged
          // publication retires nothing.
          setGuard((current) => {
            if (![...current.values()].some((baseline) => publicationVersion > baseline))
              return current;
            const next = new Map(current);
            for (const [name, baseline] of next) {
              if (publicationVersion > baseline) next.delete(name);
            }
            return next;
          });
        }}
        onMarketplaceAdded={(name) => {
          setGuard((current) => {
            if (!current.has(name)) return current;
            const next = new Map(current);
            next.delete(name);
            return next;
          });
        }}
        onRemovedMarketplace={() => setWarning(null)}
      />
    </>
  );
}

// Mounts the browser against `reject` as the hub's answer to the removal.
// The list answers the mount read with acme and every later read as already
// removed, the way a hub that applied the removal would.
async function removalUnderTest(fake: FakeClient, reject: () => Error) {
  let listCalls = 0;
  fake.on("evener/marketplace/list", () => {
    listCalls += 1;
    return { marketplaces: listCalls === 1 ? [ACME] : [] };
  });
  fake.on("evener/marketplace/browse", () => ({ name: "acme", plugins: [] }));
  fake.on("evener/marketplace/remove", () => {
    throw reject();
  });
  const client = fake as unknown as ConversationClientLike;
  const tree = render(<GuardedBrowser client={client} />);
  await act(async () => {});
  const row = tree.root.findAllByProps({ accessibilityLabel: "Browse acme" })[0];
  if (!row) throw new Error("no acme row");
  await act(async () => {
    row.props.onPress();
  });
  const remove = tree.root.findAllByProps({
    accessibilityLabel: "Remove marketplace",
  })[0];
  const removePress = remove?.props.onPress;
  if (!removePress) throw new Error("no Remove marketplace action");
  act(() => removePress());
  const request = alertRequests.at(-1);
  const confirmPress = request?.buttons?.find((button) => button.text === "Remove")?.onPress;
  if (!confirmPress) throw new Error("no Remove confirm button");
  await act(async () => {
    confirmPress();
  });
  await act(async () => {});
  return { tree, listCalls: () => listCalls };
}

it("reports an applied removal neutrally and reconciles the list", async () => {
  const fake = new FakeClient("ready");
  const { tree, listCalls } = await removalUnderTest(fake, () =>
    new WireError("marketplace removed, but the updated list was unavailable", -32603, {
      evenerErrorInfo: ErrorMarketplaceRemoveApplied,
      appliedUnavailable: true,
    }),
  );
  const text = renderedText(tree);
  expect(text).not.toContain("Could not confirm the change");
  expect(text).not.toContain("clone cleanup failed");
  // The removal stood but the list read failed, so the browser refetched and
  // the already-removed answer retired the row.
  expect(listCalls()).toBe(2);
  expect(text).not.toContain("acme");
});

it("keeps the clone-litter warning and does not refetch the reconciled list", async () => {
  const fake = new FakeClient("ready");
  const { tree, listCalls } = await removalUnderTest(fake, () =>
    new WireError("clone could not be removed", -32603, {
      evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
      applied: { marketplaces: [] },
    }),
  );
  const text = renderedText(tree);
  expect(text).toContain(
    "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.",
  );
  // The store published the rejection's authoritative applied list, so the
  // live read asks for nothing: the mount's list call is the only one.
  expect(listCalls()).toBe(1);
  expect(text).not.toContain("acme");
});

it("keeps the retryable write-failed copy for an ordinary failure", async () => {
  const fake = new FakeClient("ready");
  const { tree, listCalls } = await removalUnderTest(fake, () =>
    new Error("remove failed"),
  );
  expect(renderedText(tree)).toContain("Could not confirm the change");
  expect(listCalls()).toBe(1);
});

it("keeps the write-failed copy when a blocked install press never runs the action", async () => {
	const fake = new FakeClient("ready");
	fake.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
	fake.on("evener/marketplace/browse", () => ({
		name: "acme",
		plugins: [{ name: "tool", description: "A tool" }],
	}));
	fake.on("evener/plugin/list", () => ({ plugins: [] }));
	let installs = 0;
	fake.on("evener/plugin/install", () => {
		installs += 1;
		throw new Error("install failed");
	});
	const client = fake as unknown as ConversationClientLike;
	let ready = true;
	const tree = render(
		<GuardedBrowser client={client} canUseConnection={() => ready} />,
	);
	await act(async () => {});
	const row = tree.root.findAllByProps({ accessibilityLabel: "Browse acme" })[0];
	if (!row) throw new Error("no acme row");
	await act(async () => {
		row.props.onPress();
	});
	await act(async () => {});
	const install = tree.root.findByProps({
		accessibilityLabel: "Install tool from acme",
	});
	await act(async () => {
		install.props.onPress();
	});
	await act(async () => {});
	expect(installs).toBe(1);
	expect(renderedText(tree)).toContain("Could not confirm the change");

	// Readiness is lost between the render and the gate's recheck, the
	// sibling AddMarketplace not-ready test's driving pattern: the press is a
	// no-op the gate refuses on readiness, so the write-failed copy from the
	// install that DID run must survive - a press that ran nothing may not
	// retire the copy the user was reading.
	ready = false;
	const again = tree.root.findByProps({
		accessibilityLabel: "Install tool from acme",
	});
	act(() => again.props.onPress());
	await act(async () => {});
	expect(installs).toBe(1);
	expect(renderedText(tree)).toContain("Could not confirm the change");
});

it("keeps the write-failed copy when a blocked removal press never runs the action", async () => {
  const fake = new FakeClient("ready");
  let removals = 0;
  fake.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  fake.on("evener/marketplace/browse", () => ({ name: "acme", plugins: [] }));
  fake.on("evener/marketplace/remove", () => {
    removals += 1;
    throw new Error("remove failed");
  });
  // The confirm passed its own recheck; the gate rechecks the same
  // predicate once more, after readiness was lost between the two - the
  // sibling cleanup-warning not-ready test's counting pattern. The first
  // removal consumes the first three checks (the action's guard, the
  // confirmation's, the gate's recheck); the second press loses readiness
  // on the gate's recheck, so nothing runs.
  let readinessChecks = 0;
  const client = fake as unknown as ConversationClientLike;
  const tree = render(
    <GuardedBrowser
      client={client}
      canUseConnection={() => ++readinessChecks <= 5}
    />,
  );
  await act(async () => {});
  const row = tree.root.findAllByProps({ accessibilityLabel: "Browse acme" })[0];
  if (!row) throw new Error("no acme row");
  await act(async () => {
    row.props.onPress();
  });
  const remove = tree.root.findAllByProps({
    accessibilityLabel: "Remove marketplace",
  })[0];
  const removePress = remove?.props.onPress;
  if (!removePress) throw new Error("no Remove marketplace action");
  act(() => removePress());
  const firstRequest = alertRequests.at(-1);
  const firstConfirm = firstRequest?.buttons?.find(
    (button) => button.text === "Remove",
  )?.onPress;
  if (!firstConfirm) throw new Error("no Remove confirm button");
  await act(async () => {
    firstConfirm();
  });
  await act(async () => {});
  expect(removals).toBe(1);
  expect(renderedText(tree)).toContain("Could not confirm the change");

  // Readiness is lost at the gate's recheck, so the confirm runs nothing
  // and the removal is a no-op: the write-failed copy from the removal
  // that DID run must survive - a press that ran nothing may not retire
  // the copy the user was reading.
  const again = tree.root.findAllByProps({
    accessibilityLabel: "Remove marketplace",
  })[0];
  if (!again || again.props.disabled)
    throw new Error("Remove marketplace is not pressable");
  act(() => again.props.onPress());
  const secondRequest = alertRequests.at(-1);
  const secondConfirm = secondRequest?.buttons?.find(
    (button) => button.text === "Remove",
  )?.onPress;
  if (!secondConfirm) throw new Error("no Remove confirm button");
  await act(async () => {
    secondConfirm();
  });
  await act(async () => {});
  expect(removals).toBe(1);
  expect(renderedText(tree)).toContain("Could not confirm the change");
});

it("keeps the cleanup warning when a removal never runs (readiness lost at the gate)", async () => {
  const fake = new FakeClient("ready");
  let removals = 0;
  fake.on("evener/marketplace/list", () => ({
    // The mount read and the reconciliation read both answer with the
    // stale row: the reconciliation read is the first authoritative read
    // after the applied outcome, so the fallback ruling retires the fence
    // for it - Remove re-enables under a cleanup warning that still stands.
    marketplaces: [ACME],
  }));
  fake.on("evener/marketplace/browse", () => ({ name: "acme", plugins: [] }));
  fake.on("evener/marketplace/remove", () => {
    removals += 1;
    // The hub applied the removal but its clone cleanup failed, and the
    // answer's applied list is unavailable: the outcome records a fence and
    // raises the screen-level warning.
    throw new WireError("clone could not be removed", -32603, {
      evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
    });
  });
  // The press passed whenReady's own recheck; the gate rechecks the same
  // predicate once more, after readiness was lost between the two - the
  // sibling AddMarketplace not-ready test's driving pattern, since the
  // production path is synchronous-latent. The first removal consumes the
  // first three checks (the action's guard, the confirmation's, the gate's
  // recheck); the second press loses readiness on the gate's recheck, so
  // nothing runs.
  let readinessChecks = 0;
  const client = fake as unknown as ConversationClientLike;
  const tree = render(
    <GuardedBrowser
      client={client}
      canUseConnection={() => ++readinessChecks <= 5}
    />,
  );
  await act(async () => {});
  const row = tree.root.findAllByProps({ accessibilityLabel: "Browse acme" })[0];
  if (!row) throw new Error("no acme row");
  await act(async () => {
    row.props.onPress();
  });
  const remove = tree.root.findAllByProps({
    accessibilityLabel: "Remove marketplace",
  })[0];
  const removePress = remove?.props.onPress;
  if (!removePress) throw new Error("no Remove marketplace action");
  act(() => removePress());
  const firstRequest = alertRequests.at(-1);
  const firstConfirm = firstRequest?.buttons?.find(
    (button) => button.text === "Remove",
  )?.onPress;
  if (!firstConfirm) throw new Error("no Remove confirm button");
  await act(async () => {
    firstConfirm();
  });
  await act(async () => {});
  expect(renderedText(tree)).toContain(
    "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.",
  );

  // The fence retired with the reconciliation read, so Remove re-enables.
  // Press it again, and lose readiness at the gate's recheck: no removal
  // ran, so nothing may report one - the outstanding cleanup warning is
  // about clone files that are still the hub's business.
  const again = tree.root.findAllByProps({
    accessibilityLabel: "Remove marketplace",
  })[0];
  if (again?.props.disabled) throw new Error("Remove marketplace did not re-enable");
  act(() => again.props.onPress());
  const secondRequest = alertRequests.at(-1);
  const secondConfirm = secondRequest?.buttons?.find(
    (button) => button.text === "Remove",
  )?.onPress;
  if (!secondConfirm) throw new Error("no Remove confirm button");
  await act(async () => {
    secondConfirm();
  });
  await act(async () => {});
  expect(removals).toBe(1);
  expect(renderedText(tree)).toContain(
    "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.",
  );
  // The not-ready outcome is neither a refusal nor a failure, and it left
  // the retired fence exactly as it was: Remove stays pressable.
  expect(renderedText(tree)).not.toContain("Could not confirm the change");
  expect(
    tree.root.findAllByProps({ accessibilityLabel: "Remove marketplace" })[0]
      ?.props.disabled,
  ).toBe(false);
});

it("keeps the fence while the outcome's reconciliation read is on the wire", async () => {
  const fake = new FakeClient("ready");
  let listCalls = 0;
  let releaseRead!: (value: { marketplaces: MarketplaceEntry[] }) => void;
  const pendingRead = new Promise<{ marketplaces: MarketplaceEntry[] }>(
    (resolve) => {
      releaseRead = (value) => resolve(value);
    },
  );
  fake.on("evener/marketplace/list", () => {
    listCalls += 1;
    return listCalls === 1 ? { marketplaces: [ACME] } : pendingRead;
  });
  fake.on("evener/marketplace/browse", () => ({ name: "acme", plugins: [] }));
  fake.on("evener/marketplace/remove", () => {
    // The hub applied the removal but its clone cleanup failed, and the
    // answer's applied list is unavailable: the outcome records a fence
    // against the mount read's publication and asks the store to reconcile.
    throw new WireError("clone could not be removed", -32603, {
      evenerErrorInfo: "marketplaceUnregisteredCloneRemains",
    });
  });
  const client = fake as unknown as ConversationClientLike;
  const tree = render(<GuardedBrowser client={client} />);
  await act(async () => {});
  const row = tree.root.findAllByProps({ accessibilityLabel: "Browse acme" })[0];
  if (!row) throw new Error("no acme row");
  await act(async () => {
    row.props.onPress();
  });
  const remove = tree.root.findAllByProps({
    accessibilityLabel: "Remove marketplace",
  })[0];
  const removePress = remove?.props.onPress;
  if (!removePress) throw new Error("no Remove marketplace action");
  act(() => removePress());
  const request = alertRequests.at(-1);
  const confirmPress = request?.buttons?.find(
    (button) => button.text === "Remove",
  )?.onPress;
  if (!confirmPress) throw new Error("no Remove confirm button");
  await act(async () => {
    confirmPress();
  });
  await act(async () => {});

  // The fence recorded against the mount read's version, and its
  // reconciliation read is still on the wire: no publication newer than the
  // baseline has landed - not even the re-report the recording's own
  // re-render triggers - so the fence stands under the cleanup warning.
  expect(renderedText(tree)).toContain(
    "Marketplace removed; clone cleanup failed. Remove the leftover clone files manually.",
  );
  const fenced = tree.root.findAllByProps({
    accessibilityLabel: "Remove marketplace",
  })[0];
  expect(fenced?.props.disabled).toBe(true);
  expect(listCalls).toBe(2);

  // The read lands - still carrying the row, a re-registration the wire
  // cannot tell from the stale one - and it is the first publication newer
  // than the fence's own baseline, so the fallback ruling retires it
  // whatever it carries.
  releaseRead({ marketplaces: [ACME] });
  await act(async () => {});
  await act(async () => {});
  const again = tree.root.findAllByProps({
    accessibilityLabel: "Remove marketplace",
  })[0];
  expect(again?.props.disabled).toBe(false);
});

it("does not confirm a removal the guard fenced while the dialog was open", async () => {
  const fake = new FakeClient("ready");
  let removals = 0;
  fake.on("evener/marketplace/list", () => ({ marketplaces: [ACME] }));
  fake.on("evener/marketplace/browse", () => ({ name: "acme", plugins: [] }));
  fake.on("evener/marketplace/remove", () => {
    removals += 1;
    throw new Error("the removal should not have been issued");
  });
  const client = fake as unknown as ConversationClientLike;
  const tree = render(<GuardedBrowser client={client} />);
  await act(async () => {});
  const row = tree.root.findAllByProps({ accessibilityLabel: "Browse acme" })[0];
  if (!row) throw new Error("no acme row");
  await act(async () => {
    row.props.onPress();
  });
  const remove = tree.root.findAllByProps({
    accessibilityLabel: "Remove marketplace",
  })[0];
  const removePress = remove?.props.onPress;
  if (!removePress) throw new Error("no Remove marketplace action");
  act(() => removePress());

  // The screen fences the name while the confirmation is open, the way a
  // guard that outlives this view can: the confirm has to answer to the
  // fence the screen holds at the press, not the one it held when the
  // dialog opened.
  await act(async () => {
    tree.update(
      <GuardedBrowser client={client} fenced={new Set(["acme"])} />,
    );
  });
  const request = alertRequests.at(-1);
  const confirmPress = request?.buttons?.find(
    (button) => button.text === "Remove",
  )?.onPress;
  if (!confirmPress) throw new Error("no Remove confirm button");
  await act(async () => {
    confirmPress();
  });
  await act(async () => {});
  expect(removals).toBe(0);
  expect(renderedText(tree)).not.toContain("Could not confirm the change");
});
