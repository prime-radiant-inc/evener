import assert from 'node:assert/strict';
import test from 'node:test';
import vm from 'node:vm';
import { runtimeTraceSource, captureRuntimeTraceVerdict } from './runtime-trace.mjs';

function fixture() {
  const events = new EventTarget();
  class CustomEvent extends Event { constructor(type, options) { super(type); this.detail = options.detail; } }
  const window = { addEventListener: events.addEventListener.bind(events), dispatchEvent: events.dispatchEvent.bind(events) };
  let now = 0;
  vm.runInNewContext(runtimeTraceSource({ rootRef: 'local:root', childRef: 'local:child', entryId: 'round:fixture', path: '["delegate:fixture"]' }), { window, CustomEvent, performance: { timeOrigin: 1000, now: () => ++now } });
  const emit = (detail) => window.dispatchEvent(new CustomEvent('comicguard:trace', { detail }));
  const snapshot = () => JSON.parse(window.__comicRuntimeTrace());
  return { emit, snapshot, window, CustomEvent };
}

test('ring retains the last 2048 events with monotonic sequence and drop count', () => {
  const { emit, snapshot } = fixture();
  for (let i = 0; i < 2051; i++) emit({ ref: 'local:child', kind: 'paging', reason: 'entry', rows: i });
  const result = snapshot();
  assert.equal(result.events.length, 2048);
  assert.equal(result.dropped, 3);
  assert.equal(result.sequence, 2051);
  assert.equal(result.events[0].sequence, 4);
  assert.equal(result.events.at(-1).sequence, 2051);
  assert.equal(result.timeOrigin, 1000);
  assert.ok(result.events.at(-1).time > result.events[0].time);
  result.events[0].rows = -1;
  assert.equal(snapshot().events[0].rows, 3);
});

test('only fixture refs, known kinds and explicitly allowed primitive fields survive', () => {
  const { emit, snapshot } = fixture();
  emit({ ref: 'local:other', kind: 'paging', reason: 'entry' });
  emit({ ref: 'local:child', kind: 'secret text' });
  emit({ ref: 'local:child', kind: 'paging', reason: 'entry', rows: 9, loading: false, entryId: 'round:fixture', model: { text: 'secret' }, text: 'secret', error: 'secret', pageKey: { secret: true }, snapshot: [], offset: Infinity });
  const events = snapshot().events;
  assert.equal(events.length, 1);
  assert.deepEqual(events[0], { sequence: 1, time: 1, ref: 'local:child', kind: 'paging', reason: 'entry', rows: 9, loading: false, entryId: 'round:fixture' });
});

test('configuration exposes copied fixture identifiers only, not the ring or live owners', () => {
  const { window, CustomEvent, snapshot } = fixture();
  const detail = {};
  window.dispatchEvent(new CustomEvent('comicguard:trace-config', { detail }));
  assert.equal(detail.config.childRef, 'local:child');
  detail.config.childRef = 'local:other';
  assert.equal(snapshot().fixture.childRef, 'local:child');
  assert.deepEqual(Object.keys(window).sort(), ['__comicRuntimeTrace', 'addEventListener', 'dispatchEvent']);
});

for (const fails of [false, true]) {
  test(`final verdict retains post-hydration events and ${fails ? 'rethrows the original failure' : 'returns the result'}`, async () => {
    const { emit, snapshot } = fixture();
    const dumps = [];
    const failure = new Error('original assertion');
    const result = captureRuntimeTraceVerdict(async () => {
      await captureRuntimeTraceVerdict(async () => {
        emit({ ref: 'local:child', kind: 'transition', reason: 'placed' });
      }, (verdict) => dumps.push({ stage: 'initial', verdict, trace: snapshot() }));
      emit({ ref: 'local:child', kind: 'child', reason: 'publish', pageKey: '96', snapshotHasTarget: false });
      emit({ ref: 'local:child', kind: 'owner', reason: 'request-before', pageKey: '96' });
      emit({ ref: 'local:child', kind: 'owner', reason: 'request-before', pageKey: '56' });
      if (fails) throw failure;
      return 'original result';
    }, (verdict) => dumps.push({ stage: 'final', verdict, trace: snapshot() }));
    if (fails) await assert.rejects(result, error => error === failure);
    else assert.equal(await result, 'original result');
    assert.equal(dumps.length, 2);
    assert.equal(dumps[0].trace.sequence, 1);
    assert.equal(dumps[1].trace.sequence, 4);
    assert.equal(dumps[1].verdict, fails ? 'failed' : 'passed');
    assert.deepEqual(dumps[1].trace.events.slice(1).map(event => event.pageKey), ['96', '96', '56']);
  });
}

test('dump errors never replace the original assertion or successful value', async () => {
  const failure = new Error('assertion');
  const dump = async () => { throw new Error('unavailable browser'); };
  await assert.rejects(captureRuntimeTraceVerdict(async () => { throw failure; }, dump), error => error === failure);
  assert.equal(await captureRuntimeTraceVerdict(async () => 42, dump), 42);
});

test('disabled capture directly returns the original action promise', () => {
  const result = Promise.resolve(42);
  assert.equal(captureRuntimeTraceVerdict(() => result), result);
});
