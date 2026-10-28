import assert from 'node:assert/strict';
import { test } from 'node:test';
import * as guard from './comicguard/run.mjs';

const transition = {
  width: 850, availableWidth: 835, rootFontSize: 16, readerLeft: 15, readerRight: 850,
  single: { panel: 416, probe: 416, left: 15, right: 431 },
  paired: { rowId: 'real-pair-1', panel: 410, probe: 410, left: 15, firstRight: 425, secondLeft: 440, right: 850 },
};

test('rejects a paired panel checked against a reused single probe', () => {
  assert.throws(() => guard.verifyPanelWidths({ ...transition, paired: { ...transition.paired, probe: 416 } }, 'transition'), { name: 'Error' });
});

test('accepts independently measured single and real paired geometry at the transition width', () => {
  assert.doesNotThrow(() => guard.verifyPanelWidths(transition, 'transition', true));
  assert.throws(() => guard.verifyPanelWidths({ ...transition, paired: null }, 'transition', true), { name: 'Error' });
});

test('requires the canonical error at mounted width and mode checkpoints', () => {
  const id = 'provider-item:turn-error';
  assert.doesNotThrow(() => guard.verifyCanonicalErrorMounted({ mounted: 3, parts: [{ entryId: id }], errorGroups: [] }, id, 'width-390'));
  assert.throws(() => guard.verifyCanonicalErrorMounted({ mounted: 3, parts: [{ entryId: 'another-item' }], errorGroups: [] }, id, 'plain-mode'), { name: 'Error' });
});

test('rejects a duplicate source range owned by a different virtual row', () => {
  const covered = new Map();
  const first = { rowId: 'row-a', entryId: 'source', start: 0, end: 4, text: 'abcd' };
  const duplicate = { ...first, rowId: 'row-b' };
  assert.doesNotThrow(() => guard.collectSourceSlices(covered, [first], 'abcd'));
  assert.doesNotThrow(() => guard.collectSourceSlices(covered, [first], 'abcd'));
  assert.throws(() => guard.collectSourceSlices(covered, [duplicate], 'abcd'), { name: 'Error' });
  assert.equal(covered.size, 1);
});
