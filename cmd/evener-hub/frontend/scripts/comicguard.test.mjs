import assert from 'node:assert/strict';
import test from 'node:test';
import { comicScenario, delegateLookupStep, savedSourcePlaced, verifyApprovalRecovery, verifyComposerReceipts, verifyRemoteRequests, verifyRecoveryRetirement, verifyResumeAdmission } from './comicguard/run.mjs';

const sourceAnchor = {mode:'away',sessionRef:'local:grandchild',path:['child','grandchild'],entryId:'thought',offset:305,viewportOffset:24.125};
function placedSource() {
  return {saved:structuredClone(sourceAnchor),point:{sessionRef:sourceAnchor.sessionRef,path:[...sourceAnchor.path],entryId:'thought',offset:305,y:24.125,start:300,end:400}};
}
test('source precondition requires the exact saved anchor and its measured character within 2px',()=>{
  assert.equal(savedSourcePlaced(placedSource(),sourceAnchor),true);
  const edge=placedSource();edge.point.y+=2;
  assert.equal(savedSourcePlaced(edge,sourceAnchor),true);
});
for(const [name,change] of [
  ['missing immediate pre-reload point',v=>{v.point=null;}],
  ['wrong saved entry in the same grandchild',v=>{v.saved.entryId='other';}],
  ['wrong saved character',v=>{v.saved.offset=0;}],
  ['wrong saved geometry',v=>{v.saved.viewportOffset=-103;}],
  ['wrong saved path',v=>{v.saved.path=['other','grandchild'];}],
  ['wrong saved session',v=>{v.saved.sessionRef='remote:grandchild';}],
  ['saved end intent',v=>{v.saved.mode='end';}],
  ['missing saved anchor',v=>{v.saved=null;}],
  ['wrong measured entry',v=>{v.point.entryId='other';}],
  ['wrong measured offset',v=>{v.point.offset=0;}],
  ['wrong measured path',v=>{v.point.path=['other','grandchild'];}],
  ['wrong measured session',v=>{v.point.sessionRef='remote:grandchild';}],
  ['unplaced point',v=>{v.point.y=450.75;}],
  ['outside 2px',v=>{v.point.y+=2.01;}],
])test(`source precondition rejects ${name}`,()=>{
  const value=placedSource();change(value);
  assert.equal(savedSourcePlaced(value,sourceAnchor),false);
});

test('Comic default stays complete with 102 headings, focused roster has only its 101 leaves', () => {
  assert.deepEqual(comicScenario(), {focused:false, rosterCount:102});
  assert.deepEqual(comicScenario(''), {focused:false, rosterCount:102});
  assert.deepEqual(comicScenario('remote-navigation'), {focused:true, rosterCount:101});
  assert.deepEqual(comicScenario('recovery'), {focused:true});
  assert.throws(() => comicScenario('remote'), Error);
});

const sentFrame=(payload,at=101)=>({event:'Network.webSocketFrameSent',connection:'original',at,frame:JSON.stringify(payload)});
const receivedFrame=(payload,at=102)=>({...sentFrame(payload,at),event:'Network.webSocketFrameReceived'});
function retirement() {
  return {oldRef:'local:old',connection:'original',acknowledgedAt:100,backIndex:2,documentOrigin:1,currentDocumentOrigin:1,trace:[
    sentFrame({id:1,method:'thread/unsubscribe',params:{ref:'local:old'}}),receivedFrame({id:1,result:{}}),
    sentFrame({id:2,method:'thread/read',params:{ref:'local:old',includeTurns:true}},103),
    receivedFrame({id:2,result:{thread:{evener:{resumeRequired:true,resumeOnlyFoldable:false}}}},104),
  ]};
}
function editFrame(row,edit) {const frame=JSON.parse(row.frame);edit(frame);row.frame=JSON.stringify(frame);}
test('recovery oracle accepts completed original membership before Back and a fresh fenced read',()=>{
  const result=verifyRecoveryRetirement(retirement());
  assert.equal(result.retired.request.id,1);
  assert.equal(result.fenced.request.id,2);
});
for(const [name,mutate] of [
  ['wrong retirement ref',v=>editFrame(v.trace[0],p=>{p.params.ref='local:other';})],
  ['wrong retirement socket',v=>{v.trace[0].connection=v.trace[1].connection='other';}],
  ['failed retirement',v=>editFrame(v.trace[1],p=>{p.error={code:1};})],
  ['retirement before acknowledgement',v=>{v.trace[0].at=99;}],
  ['Back before retirement response',v=>{v.backIndex=1;}],
  ['wrong read ref',v=>editFrame(v.trace[2],p=>{p.params.ref='local:other';})],
  ['wrong read socket',v=>{v.trace[2].connection=v.trace[3].connection='other';}],
  ['read before Back',v=>{v.backIndex=3;}],
  ['foldable read',v=>editFrame(v.trace[3],p=>{p.result.thread.evener.resumeOnlyFoldable=true;})],
  ['failed read carrying recovery fields',v=>editFrame(v.trace[3],p=>{p.error={code:1};})],
  ['new document',v=>{v.currentDocumentOrigin=2;}],
  ['new socket',v=>v.trace.push({event:'Network.webSocketCreated',connection:'other'})],
])test(`recovery retirement rejects ${name}`,()=>{const value=retirement();mutate(value);assert.throws(()=>verifyRecoveryRetirement(value),Error);});

function admission(released=false) {
  const request={id:3,method:'thread/resume',params:{ref:'local:old'}};
  const response={id:3,result:{thread:{id:'new-thread',evener:{ref:'local:new-thread',instanceId:'new-instance'}}}};
  const value={oldRef:'local:old',destination:'local:new-thread',connection:'original',
    trace:[sentFrame(request),receivedFrame(response)],
    hold:{socket:0,requestBytes:JSON.stringify(request),responseBytes:JSON.stringify(response),releaseCount:0,starts:[]},
    ui:{ref:'local:old',beforeText:'COMIC_RESUME_CHANGED',text:'',disabled:true}};
  if(released) {
    const start={id:4,method:'turn/start',params:{ref:'local:new-thread',expectedInstanceId:'new-instance',clientMutationId:'mutation',input:[{type:'text',text:'COMIC_RESUME_CHANGED'}]}};
    value.releaseIndex=2;
    value.hold.releaseCount=1;
    value.hold.releasedBytes=value.hold.responseBytes;
    value.hold.starts=[{socket:0,beforeRelease:false,bytes:JSON.stringify(start)}];
    value.afterUI={ref:'local:new-thread',text:'COMIC_NEWER_DESTINATION_DRAFT'};
    value.trace.push(sentFrame(start,103),receivedFrame({id:4,result:{receipt:{clientMutationId:'mutation',disposition:'applied',threadId:'new-thread',instanceId:'new-instance'}}},104));
  }
  return value;
}
test('resume oracle accepts an authentic held response with zero starts and post-enqueue editor transition',()=>{verifyResumeAdmission(admission());});
test('resume oracle accepts one released response then one correct fenced applied mutation and handoff',()=>{verifyResumeAdmission(admission(true));});
function replacementAdmission(released=false) {
  const value=admission(released);
  for(const row of value.trace)row.connection='replacement';
  value.hold.socket=1;
  for(const start of value.hold.starts)start.socket=1;
  return value;
}
for(const released of [false,true])test(`resume oracle follows the actual replacement connection, released=${released}`,()=>{
  const result=verifyResumeAdmission(replacementAdmission(released));
  assert.equal(result.resumed.connection,'replacement');
  if(released)assert.equal(result.changed.connection,'replacement');
});
for(const [name,mutate,released=false] of [
  ['reply on original socket',v=>{v.trace[1].connection='original';}],
  ['duplicate resume reply',v=>v.trace.push(v.trace[1])],
  ['wrong native request bytes',v=>{v.hold.requestBytes+=' ';}],
  ['premature replacement start',v=>v.trace.push(replacementAdmission(true).trace[2])],
  ['mutation on original socket',v=>{v.trace[2].connection=v.trace[3].connection='original';},true],
  ['mutation reply on original socket',v=>{v.trace[3].connection='original';},true],
  ['native start on original socket',v=>{v.hold.starts[0].socket=0;},true],
  ['duplicate replacement start',v=>v.trace.push(v.trace[2]),true],
])test(`replacement resume rejects ${name}`,()=>{
  const value=replacementAdmission(released);mutate(value);
  assert.throws(()=>verifyResumeAdmission(value),Error);
});
for(const [name,mutate,released=false] of [
  ['missing resume',v=>{v.trace=[];}],
  ['wrong resume ref',v=>editFrame(v.trace[0],p=>{p.params.ref='local:wrong';})],
  ['wrong connection',v=>{v.trace[1].connection='other';}],
  ['missing authentic response',v=>{v.hold.responseBytes='';}],
  ['rewritten held response',v=>{v.hold.responseBytes+=' ';}],
  ['unchanged ready UI instead of processed Send',v=>{v.ui.disabled=false;}],
  ['editor has not processed Send',v=>{v.ui.text='COMIC_RESUME_CHANGED';}],
  ['wrong input before Send',v=>{v.ui.beforeText='other';}],
  ['premature wire start',v=>v.trace.push(admission(true).trace[2])],
  ['premature native start before CDP delivery',v=>{v.hold.starts=[{socket:0,beforeRelease:true,bytes:admission(true).trace[2].frame}];}],
  ['duplicate resume',v=>v.trace.push(v.trace[0])],
  ['premature release',v=>{v.hold.releaseCount=1;}],
  ['missing start',v=>{v.trace.splice(2);},true],
  ['duplicate start',v=>v.trace.push(v.trace[2]),true],
  ['earlier wrong-instance start',v=>{const earlier=structuredClone(v.trace[2]);editFrame(earlier,p=>{p.params.expectedInstanceId='old-instance';});v.trace.unshift(earlier);v.releaseIndex++;},true],
  ['wrong destination',v=>editFrame(v.trace[2],p=>{p.params.ref='local:old';}),true],
  ['wrong instance fence',v=>editFrame(v.trace[2],p=>{p.params.expectedInstanceId='old-instance';}),true],
  ['rewritten input',v=>editFrame(v.trace[2],p=>{p.params.input[0].text='changed';}),true],
  ['mismatched receipt mutation',v=>editFrame(v.trace[3],p=>{p.result.receipt.clientMutationId='other';}),true],
  ['wrong receipt instance',v=>editFrame(v.trace[3],p=>{p.result.receipt.instanceId='old-instance';}),true],
  ['wrong receipt thread',v=>editFrame(v.trace[3],p=>{p.result.receipt.threadId='other-thread';}),true],
  ['unapplied receipt',v=>editFrame(v.trace[3],p=>{p.result.receipt.disposition='replayed';}),true],
  ['duplicate receipt',v=>v.trace.push(v.trace[3]),true],
  ['duplicate release',v=>{v.hold.releaseCount=2;},true],
  ['rewritten released bytes',v=>{v.hold.releasedBytes+=' ';},true],
  ['wrong destination route',v=>{v.afterUI.ref='local:old';},true],
  ['lost newer destination draft',v=>{v.afterUI.text='COMIC_RESUME_CHANGED';},true],
])test(`resume admission rejects ${name}`,()=>{
  const value=admission(released);mutate(value);
  // Field-negative cases keep native/CDP bytes consistent, so the identity and
  // literal-input checks themselves, not a byte mismatch, must reject them.
  if(['wrong destination','wrong instance fence','rewritten input'].includes(name))value.hold.starts[0].bytes=value.trace[2].frame;
  assert.throws(()=>verifyResumeAdmission(value),Error);
});

const selected = {ref:'comic-remote:child', key:'remote-root/delegate'};
function scanTarget(top, targetY, viewport = 240) {
  const height = 2400;
  let direction = -1;
  const steps = [];
  for (let i = 0; i < 50; i++) {
    const mounted = targetY >= top && targetY < top + viewport ? [selected] : [];
    const step = delegateLookupStep({top,height,viewport,mounted}, selected, direction);
    steps.push({...step,top});
    if (step.index >= 0) return {found:true,steps};
    assert.ok(Math.abs(step.deltaY) < viewport, 'scroll steps must overlap, not skip viewport ranges');
    direction = step.direction;
    top = Math.max(0,Math.min(height-viewport,top+step.deltaY));
  }
  return {found:false,steps};
}
test('delegate lookup reaches a target initially below after reaching the upper boundary', () => {
  assert.equal(scanTarget(500,1900,641).found, true);
});
test('delegate lookup reaches a target initially above', () => {
  assert.equal(scanTarget(1500,200,641).found, true);
});
test('delegate lookup reverses at either boundary', () => {
  assert.ok(delegateLookupStep({top:0,height:2000,viewport:600,mounted:[]}, selected, -1).deltaY > 0);
  assert.ok(delegateLookupStep({top:1400,height:2000,viewport:600,mounted:[]}, selected, 1).deltaY < 0);
});
test('delegate lookup overlaps narrow viewport ranges and never finds a missing target', () => {
  assert.equal(scanTarget(1000,1800).found, true);
  assert.equal(scanTarget(1000,-1).found, false);
});
test('delegate lookup requires the selected qualified ref AND strip key', () => {
  const observation = {top:0,height:2000,viewport:600,mounted:[
    {...selected,ref:'local:child'}, {...selected,key:'another-root/delegate'},
  ]};
  assert.equal(delegateLookupStep(observation,selected).index, -1);
  observation.mounted.push(selected);
  assert.equal(delegateLookupStep(observation,selected).index, 2);
});

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
test('remote oracle excludes preceding document reads but rejects local reads on its own connection', () => {
  const sent=(connection,ref)=>({event:'Network.webSocketFrameSent',connection,frame:JSON.stringify({method:'thread/read',params:{ref}})});
  const created={event:'Network.webSocketCreated',connection:'remote-document'};
  assert.equal(verifyRemoteRequests([sent('old-document','local:old'),created,sent('remote-document','comic-remote:root')]).length,1);
  assert.throws(()=>verifyRemoteRequests([created,sent('remote-document','local:wrong')]));
});
for (const method of ['thread/read','turn/start','thread/resume']) {
  for (const connection of ['destination','reconnected']) {
    test(`remote oracle rejects ${method} on ${connection}`, () => {
      const rows=['destination','reconnected'].map(connection=>({event:'Network.webSocketCreated',connection}));
      rows.push({event:'Network.webSocketFrameSent',connection,frame:JSON.stringify({method,params:{ref:method==='thread/read'?'local:wrong':'comic-remote:root'}})});
      assert.throws(()=>verifyRemoteRequests(rows));
    });
  }
}
test('remote oracle retains mutation prohibition for preceding connection', () => {
  assert.throws(()=>verifyRemoteRequests([{event:'Network.webSocketCreated',connection:'destination'},{event:'Network.webSocketFrameSent',connection:'old-document',frame:JSON.stringify({method:'turn/start',params:{ref:'local:old'}})}]));
  assert.throws(()=>verifyRemoteRequests([]));
});

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
