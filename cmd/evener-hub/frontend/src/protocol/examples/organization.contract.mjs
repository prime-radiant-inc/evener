import assert from "node:assert/strict";
import test from "node:test";
import { readNavigation, runOrganization } from "./organization-logic.mjs";

test("keeps unrelated pin-section receipt revisions away from the location revision", async () => {
  await optedIn(async () => {
    const f = fixture({
      mutation: {
        ok: true,
        changed: false,
        navigation: {
          generation_id: "g",
          targets: [
            { kind: "pin_catalog", revision: 1 },
            { kind: "pin_section", sectionId: "focus", revision: 900 },
          ],
        },
      },
    });
    const result = await runOrganization(f.hub, { action: "assign", target, ownedHub: url });
    assert.equal(result.outcome, "applied");
    assert.equal(result.after.versions.location.revision, 1);
  });
});
test("requires a resource root and all of its referenced entities", async () => {
  for (const damage of ["root", "child"]) {
    const f = fixture();
    const request = f.hub.request;
    f.hub.request = async (method, params) => {
      const r = await request(method, params);
      if (params.resource === "pin_catalog") {
        if (damage === "root") r.data.containers = [];
        else r.data.containers[0].children.push("missing-child");
      }
      return r;
    };
    await assert.rejects(readNavigation(f.hub));
  }
});
test("checks the manifest receipt floor independently of the pin catalog", async () => {
  await optedIn(async () => {
    const f = fixture({
      mutation: {
        ok: true,
        changed: true,
        navigation: {
          generation_id: "g",
          targets: [
            { kind: "manifest", revision: 900 },
            { kind: "pin_catalog", revision: 1 },
          ],
        },
      },
    });
    const result = await runOrganization(f.hub, { action: "delete", target: { sectionId: "focus" }, ownedHub: url });
    assert.equal(result.outcome, "ackReadbackFailed");
  });
});

const url = "ws://127.0.0.1:1/rpc";
const target = { sessionRef: "opaque-session", sectionId: "focus" };
const response = (data = [], revision = 1, resource = "pin_catalog", ref) => {
  const entities = data.map((value, index) => ({
    key: `e${index}`,
    kind: resource === "location" ? "session" : "pin_section",
    value,
  }));
  const slot = resource === "pin_catalog" ? "pin_sections" : resource === "location" ? "session" : "manifest";
  return {
    status: "ok",
    representation: "snapshot",
    generationId: "g",
    revision,
    etag: `${resource}-${revision}`,
    data: {
      metadata: {
        generation_id: "g",
        revision,
        remaining: 0,
        ...(resource === "location" ? { ref, top_level_ref: ref, top_level: true, pin_section_id: "focus" } : {}),
      },
      entities,
      containers: slot
        ? [{ key: "root", owner: { kind: "resource_root", slot }, children: entities.map((entity) => entity.key) }]
        : [],
    },
  };
};
function fixture({ mutation, failReadAfter = false } = {}) {
  const calls = [];
  let reads = 0;
  return {
    calls,
    hub: {
      async request(method, params) {
        calls.push({ method, params });
        if (method === "evener/navigation/read") {
          reads += 1;
          if (failReadAfter && reads > 5) throw new Error("private transport detail");
          if (params.resource === "manifest") return response([], 8, "manifest");
          if (params.resource === "pin_catalog" && params.offset === 0)
            return {
              ...response([{ id: "focus", name: "Focus", count: 1 }]),
              data: {
                ...response([{ id: "focus", name: "Focus", count: 1 }]).data,
                metadata: { generation_id: "g", revision: 1, remaining: 1 },
              },
            };
          if (params.resource === "pin_catalog")
            return { ...response([{ id: "later", name: "Later", count: 0 }]), etag: "catalog-next-page" };
          return response(
            [
              {
                ref: params.ref,
                host_id: "local",
                session_id: "s",
                title: "Session",
                project: "p",
                state: "idle",
                kind: "session",
              },
            ],
            1,
            "location",
            params.ref,
          );
        }
        if (mutation) return mutation;
        throw new Error("unexpected mutation");
      },
    },
  };
}
async function optedIn(run) {
  const old = process.env.EVENER_ORGANIZATION_MUTATION;
  const oldUrl = process.env.EVENER_RPC_URL;
  process.env.EVENER_ORGANIZATION_MUTATION = "1";
  process.env.EVENER_RPC_URL = url;
  try {
    await run();
  } finally {
    if (old === undefined) delete process.env.EVENER_ORGANIZATION_MUTATION;
    else process.env.EVENER_ORGANIZATION_MUTATION = old;
    if (oldUrl === undefined) delete process.env.EVENER_RPC_URL;
    else process.env.EVENER_RPC_URL = oldUrl;
  }
}
test("reads v2 catalog pages and session location around one changed:false mutation", async () => {
  await optedIn(async () => {
    const f = fixture({ mutation: { ok: true, changed: false, navigation: { generation_id: "g", targets: [] } } });
    const result = await runOrganization(f.hub, { action: "assign", target, ownedHub: url });
    assert.equal(result.outcome, "applied");
    assert.equal(result.changed, false);
    assert.equal(f.calls.filter((call) => call.method === "evener/session-pin/assign").length, 1);
    assert.equal(f.calls.filter((call) => call.method === "evener/navigation/read").length, 8);
    assert.equal(f.calls[2].params.limit, 40);
  });
});
test("refuses missing ownership, ambiguous targets, and overlong names before requests", async () => {
  const f = fixture();
  await assert.rejects(runOrganization(f.hub, { action: "unpin", target, ownedHub: url }));
  await optedIn(async () => {
    await assert.rejects(runOrganization(f.hub, { action: "toString", target, ownedHub: url }));
    await assert.rejects(
      runOrganization(f.hub, {
        action: "assign",
        target: { sessionRef: "s", sectionId: "a", sectionName: "b" },
        ownedHub: url,
      }),
    );
    await assert.rejects(
      runOrganization(f.hub, { action: "rename", target: { sectionId: "a", name: "x".repeat(81) }, ownedHub: url }),
    );
  });
  assert.equal(f.calls.length, 0);
});
test("reports known rejection without treating ok:false as applied", async () => {
  await optedIn(async () => {
    const f = fixture({ mutation: { ok: false, changed: false, navigation: { generation_id: "g", targets: [] } } });
    const result = await runOrganization(f.hub, { action: "delete", target: { sectionId: "focus" }, ownedHub: url });
    assert.equal(result.outcome, "rejected");
  });
});
test("distinguishes lost mutation reply and acknowledged readback failure without replay", async () => {
  await optedIn(async () => {
    const lost = fixture();
    lost.hub.request = async (method, params) => {
      lost.calls.push({ method, params });
      if (method === "evener/navigation/read") return fixture().hub.request(method, params);
      throw new Error("private transport detail");
    };
    const result = await runOrganization(lost.hub, { action: "delete", target: { sectionId: "focus" }, ownedHub: url });
    assert.equal(result.outcome, "unknownMutation");
    assert.equal(lost.calls.filter((call) => call.method === "evener/pin-section/delete").length, 1);
    const ack = fixture({
      mutation: { ok: true, changed: true, navigation: { generation_id: "g", targets: [] } },
      failReadAfter: true,
    });
    const ackResult = await runOrganization(ack.hub, {
      action: "delete",
      target: { sectionId: "focus" },
      ownedHub: url,
    });
    assert.equal(ackResult.outcome, "ackReadbackFailed");
  });
});

test("classifies stale generation and malformed acknowledgement as uncertain", async () => {
  await optedIn(async () => {
    const stale = fixture({ mutation: { ok: true, changed: true, navigation: { generation_id: "new", targets: [] } } });
    const staleResult = await runOrganization(stale.hub, {
      action: "delete",
      target: { sectionId: "focus" },
      ownedHub: url,
    });
    assert.equal(staleResult.outcome, "ackReadbackFailed");
    const malformed = fixture({ mutation: { ok: true, navigation: { generation_id: "g", targets: [] } } });
    const malformedResult = await runOrganization(malformed.hub, {
      action: "delete",
      target: { sectionId: "focus" },
      ownedHub: url,
    });
    assert.equal(malformedResult.outcome, "ackReadbackFailed");
  });
});

test("rejects readback below a receipt target revision floor", async () => {
  await optedIn(async () => {
    const f = fixture({
      mutation: {
        ok: true,
        changed: true,
        navigation: { generation_id: "g", targets: [{ kind: "pin_catalog", revision: 2 }] },
      },
    });
    const result = await runOrganization(f.hub, { action: "delete", target: { sectionId: "focus" }, ownedHub: url });
    assert.equal(result.outcome, "ackReadbackFailed");
  });
});
