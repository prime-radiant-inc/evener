import assert from "node:assert/strict";
import test from "node:test";
import { WireError } from "../dist/index.js";
import { inspectPage, reconcilePage, recoverAfterStaleCursor } from "./streaming-rejoin-logic.mjs";

const item = (key, entry, position = 0) => ({ transcriptKey: key, position: { entry, item: position } });

test("page inspection uses explicit turn boundaries and validates ordered identity", () => {
  const result = inspectPage([{ hasEarlierItems: true, hasLaterItems: false, items: [item("a", 1), item("b", 1, 1)] }]);
  assert.deepEqual(result, { items: 2, stableKeys: 2, positioned: 2, boundaries: 1 });
  assert.throws(() => inspectPage([{ items: [item("a", 2), item("b", 1)] }]));
  assert.throws(() => inspectPage([{ items: [item("a", 1), item("a", 1, 1)] }]));
});

test("reconciliation deduplicates overlapping pages and retains ordered current payload", () => {
  const merged = reconcilePage(
    [item("b", 2), { ...item("a", 1), text: "current" }],
    [item("a", 1), item("old", 0)],
  );
  assert.deepEqual(merged.map((entry) => entry.transcriptKey), ["old", "a", "b"]);
  assert.equal(merged[1].text, "current");
});

test("a stale cursor triggers a fresh subscribed snapshot without replay", async () => {
  const calls = [];
  const hub = { request: async (method, params) => {
    calls.push({ method, params });
    if (method === "thread/turns/list") throw new WireError("transcript item cursor is stale; refresh the thread", -32602, { evenerErrorInfo: "transcriptItemCursorStale" });
    return { thread: { evener: { ref: "ref_fixture", instanceId: "instance_2" }, turns: [] } };
  } };
  const snapshot = await recoverAfterStaleCursor(hub, "ref_fixture", "opaque-old-cursor");
  assert.equal(snapshot.snapshot.thread.evener.ref, "ref_fixture");
  assert.deepEqual(calls.map(({ method }) => method), ["thread/turns/list", "thread/read"]);
  assert.equal("clientMutationId" in calls[1].params, false);
  assert.equal(calls[1].params.replaceSubscription, true);
});

test("system prelude position zero is valid and unkeyed items are retained", () => {
  assert.equal(inspectPage([{items:[item("system", 0)]}]).positioned, 1);
  const one = {id:"one",text:"first"};
  const two = {id:"two",text:"second"};
  assert.deepEqual(reconcilePage([one, two], []), [one, two]);
});

test("the runnable read workflow recovers stale paging and consumes routed resets", async () => {
  const {runReadRecipe} = await import("./streaming-rejoin-logic.mjs");
  let listener;
  let reads = 0;
  const calls = [];
  const response = (key, olderCursor) => ({thread:{evener:{ref:"local:fixture",instanceId:"same"},turns:[{id:"turn",items:[item(key, 1)],hasEarlierItems:false,hasLaterItems:false}]},olderCursor});
  const hub = {
    onNotification(fn) {listener=fn;return ()=>{listener=undefined;};},
    async request(method,params) {
      calls.push({method,params});
      if(method==="thread/read") {reads++;return response(`read-${reads}`, reads===1?"opaque":undefined);}
      if(method==="thread/turns/list") throw new WireError("stale",-32602,{evenerErrorInfo:"transcriptItemCursorStale"});
      return {};
    },
  };
  const result=await runReadRecipe(hub,"local:fixture",async()=>{
    listener({method:"item/agentMessage/reset",params:{ref:"local:fixture",threadId:"fixture"}});
    listener({method:"evener/thread/resync",params:{ref:"other:fixture",threadId:"fixture"}});
  });
  assert.equal(result.staleCursorRecovered,true);
  assert.deepEqual(result.notifications,["item/agentMessage/reset"]);
  assert.equal(result.reconciledItems[0].transcriptKey,"read-3");
  assert.equal(result.rejoinedItems[0].transcriptKey,"read-4");
  assert.equal(listener,undefined);
  assert.ok(calls.every(x=>["thread/read","thread/turns/list","thread/unsubscribe"].includes(x.method)));
  assert.ok(calls.filter(x=>x.method!=="thread/unsubscribe").every(x=>x.params.itemLimit<=40));
});

test("opaque owned refs work and cleanup retains a primary failure", async () => {
  const {runReadRecipe} = await import("./streaming-rejoin-logic.mjs");
  const failure = new Error("read failed");
  const cleanup = new Error("cleanup failed");
  const calls=[];
  const hub={onNotification:()=>()=>{},async request(method){
    calls.push(method);if(method==="thread/read")throw failure;throw cleanup;
  }};
  await assert.rejects(runReadRecipe(hub,"simulator-scripted:owned"), error =>
    error instanceof AggregateError && error.errors.includes(failure) && error.errors.includes(cleanup));
  assert.deepEqual(calls,["thread/read","thread/unsubscribe"]);
});

test("successful paging preserves overlaps and ignores another session's events", async () => {
  const {runReadRecipe}=await import("./streaming-rejoin-logic.mjs");
  let listener;let reads=0;
  const response={thread:{evener:{ref:"fixture:owned",instanceId:"same"},turns:[{items:[{...item("b",1),text:"current"}]}]},olderCursor:"opaque"};
  const hub={onNotification(fn){listener=fn;return ()=>{};},async request(method){
    if(method==="thread/read"){reads++;return response;}
    if(method==="thread/turns/list")return {data:[{items:[item("a",0),{...item("b",1),text:"older"}]}]};
    return {};
  }};
  const result=await runReadRecipe(hub,"fixture:owned",async()=>listener({method:"evener/thread/resync",params:{ref:"fixture:other"}}));
  assert.equal(result.paged,true);
  assert.equal(result.staleCursorRecovered,false);
  assert.deepEqual(result.reconciledItems.map(x=>x.transcriptKey),["a","b"]);
  assert.equal(result.reconciledItems[1].text,"current");
  assert.equal(reads,2);
  assert.deepEqual(result.notifications,[]);
});
