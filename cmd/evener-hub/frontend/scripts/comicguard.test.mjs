import assert from 'node:assert/strict';
import test from 'node:test';
import { verifyApprovalRecovery, verifyComposerReceipts } from './comicguard/run.mjs';

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

const failedApproval = {id:19, method:'evener/sandbox/escalation/resolve', params:{ref:'local:root', escalationId:'approval-1', approve:true}};
function recovery() {
  return {
    busy:{ref:'local:root', disabled:[true,true]},
    retry:{ref:'local:root', disabled:[false,false]},
    wireTrace:[{event:'Network.webSocketClosed', connection:'socket'}],
  };
}

test('approval guard accepts observed busy then retry recovery without copy', () => {
  verifyApprovalRecovery(recovery(), failedApproval);
});
for (const [name, mutate] of [
  ['missing busy observation', value => { delete value.busy; }],
  ['never busy', value => { value.busy.disabled = [false,false]; }],
  ['missing retry observation', value => { delete value.retry; }],
  ['retry remains disabled', value => { value.retry.disabled = [true,true]; }],
  ['partially enabled retry', value => { value.retry.disabled = [false,true]; }],
  ['disappeared controls', value => { value.retry.disabled = []; }],
  ['another root recovery', value => { value.retry.ref = 'local:other'; }],
  ['missing native close', value => { value.wireTrace = []; }],
  ['approval sent before retry', value => { value.wireTrace.push({event:'Network.webSocketFrameSent',connection:'socket',frame:JSON.stringify(failedApproval)}); }],
]) test(`approval guard rejects ${name}`, () => {
  const value = recovery();
  mutate(value);
  assert.throws(() => verifyApprovalRecovery(value, failedApproval));
});
