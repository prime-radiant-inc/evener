import assert from 'node:assert/strict';
import test from 'node:test';
import { verifyComposerReceipts } from './comicguard/run.mjs';

function trace() {
  return ['turn/start', 'turn/queue', 'turn/interrupt'].flatMap((method, index) => {
    const clientMutationId = `mutation-${index}`;
    const request = {id:index, method, params:{ref:'local:root', clientMutationId}};
    if (index < 2) request.params.input = [{type:'text', text:index === 0 ? '/settings COMIC_COMPOSER_SEND literal input' : 'COMIC_COMPOSER_QUEUE literal queued input'}];
    const response = {id:index, result:{receipt:{clientMutationId, disposition:'applied', threadId:'thread', ...(index === 1 ? {queueEntryIds:['queue-1']} : {})}}};
    return [{event:'Network.webSocketFrameSent', connection:'socket', frame:JSON.stringify(request)}, {event:'Network.webSocketFrameReceived', connection:'socket', frame:JSON.stringify(response)}];
  });
}

test('composer guard requires correlated literal mutation receipts', () => {
  assert.equal(verifyComposerReceipts(trace(), 'local:root').length, 3);
});
for (const [name, mutate] of [
  ['missing Stop receipt', rows => rows.pop()],
  ['wrong connection', rows => { rows[1].connection = 'another'; }],
  ['duplicate receipt', rows => rows.push(rows[1])],
  ['duplicate unacknowledged request', rows => rows.push({...rows[0], connection:'other'})],
  ['wrong ref', rows => { const frame = JSON.parse(rows[0].frame); frame.params.ref = 'local:other'; rows[0].frame = JSON.stringify(frame); }],
  ['rewritten slash input', rows => { const frame = JSON.parse(rows[0].frame); frame.params.input[0].text = 'settings'; rows[0].frame = JSON.stringify(frame); }],
  ['mismatched mutation', rows => { const frame = JSON.parse(rows[1].frame); frame.result.receipt.clientMutationId = 'other'; rows[1].frame = JSON.stringify(frame); }],
  ['missing queue entry', rows => { const frame = JSON.parse(rows[3].frame); delete frame.result.receipt.queueEntryIds; rows[3].frame = JSON.stringify(frame); }],
]) test(`composer guard rejects ${name}`, () => {
  const rows = trace();
  mutate(rows);
  assert.throws(() => verifyComposerReceipts(rows, 'local:root'));
});
