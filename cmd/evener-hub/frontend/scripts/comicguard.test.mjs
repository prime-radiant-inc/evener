import assert from 'node:assert/strict';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import { cachedReloadRecovery, comicScenario, delegateLookupStep, recordReloadEvents, sampleCachedReloadRecovery, savedSourcePlaced, servedDocumentAnchorReady, verifyApprovalRecovery, verifyComposerReceipts, verifyRemoteRequests, verifyRecoveryRetirement, verifyResumeAdmission } from './comicguard/run.mjs';

test('document readiness waits for cached relative links to acquire live source authority',()=>{
  assert.equal(servedDocumentAnchorReady(undefined),false);
  assert.equal(servedDocumentAnchorReady(''),false);
  assert.equal(servedDocumentAnchorReady('http://127.0.0.1:34211/s/comic-document-child.md'),false);
  assert.equal(servedDocumentAnchorReady('http://127.0.0.1:34211/doc/file?session=local%3Achild&path=%2Fchild%2Fcomic-document-child.md'),true);
});

test('served readiness leaves wrong source and path rejection to the separate ownership assertion',()=>{
  assert.equal(servedDocumentAnchorReady('http://127.0.0.1:34211/doc/file?session=local%3Awrong&path=%2Fwrong.md'),true);
  assert.equal(servedDocumentAnchorReady('http://127.0.0.1:34211/doc/file-other?session=local%3Achild'),false);
});

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
function cachedRecovery() {
  return {documentOrigin:100,currentDocumentOrigin:100,ref:sourceAnchor.sessionRef,expected:sourceAnchor,geometry:placedSource(),intent:[{type:'scroll'}],trace:[
    {event:'Network.webSocketCreated',connection:'original',at:100},
    sentFrame({id:1,method:'thread/read',params:{ref:sourceAnchor.sessionRef,includeTurns:true}}),
    receivedFrame({id:1,result:{thread:{}}}),
    sentFrame({id:2,method:'thread/turns/list',params:{ref:sourceAnchor.sessionRef,cursor:'older'}},103),
    receivedFrame({id:2,result:{items:[]}},104),
  ]};
}
test('cached recovery accepts post-hydration page completion and the same source without input',()=>{
  const evidence=cachedReloadRecovery(cachedRecovery());
  assert.equal(evidence.ready,true);
  assert.equal(evidence.hydration.request.id,1);
  assert.equal(evidence.pages.length,1);
  assert.equal(evidence.pages[0].response.id,2);
});
for(const [name,mutate] of [
  ['cached placement and hydration reply without an application witness',v=>v.trace.splice(3)],
  ['older page demand before hydration reply',v=>{[v.trace[2],v.trace[3]]=[v.trace[3],v.trace[2]];}],
  ['failed hydration',v=>editFrame(v.trace[2],p=>{p.error={code:1};})],
  ['metadata-only read',v=>editFrame(v.trace[1],p=>{p.params.includeTurns=false;})],
  ['wrong child',v=>editFrame(v.trace[1],p=>{p.params.ref='local:other';})],
  ['old document connection',v=>{v.trace[0].at=99;}],
  ['wrong response connection',v=>{v.trace[2].connection='other';}],
  ['unfinished older page',v=>v.trace.pop()],
  ['failed older page',v=>editFrame(v.trace[4],p=>{p.error={code:1};})],
  ['another pending child page',v=>v.trace.push(sentFrame({id:3,method:'thread/turns/list',params:{ref:sourceAnchor.sessionRef}},105))],
  ['another pending child refresh',v=>v.trace.push(sentFrame({id:3,method:'thread/read',params:{ref:sourceAnchor.sessionRef,includeTurns:true}},105))],
  ['new document',v=>{v.currentDocumentOrigin=200;}],
  ['replacement connection',v=>v.trace.push({event:'Network.webSocketCreated',connection:'replacement',at:105})],
  ['closed connection',v=>v.trace.push({event:'Network.webSocketClosed',connection:'original',at:105})],
  ['reader input',v=>v.intent.push({type:'wheel'})],
  ['missing final source',v=>{v.geometry.point=null;}],
  ['changed saved offset',v=>{v.geometry.saved.offset=0;}],
])test(`cached recovery rejects ${name}`,()=>{
  const value=cachedRecovery();mutate(value);
  assert.equal(cachedReloadRecovery(value).ready,false);
});
test('cached recovery waits for all resulting pages without requiring a fixed count',()=>{
  const value=cachedRecovery();
  value.trace.push(sentFrame({id:3,method:'thread/turns/list',params:{ref:sourceAnchor.sessionRef}},105),receivedFrame({id:3,result:{items:[]}},106));
  const evidence=cachedReloadRecovery(value);
  assert.equal(evidence.ready,true);
  assert.equal(evidence.pages.length,2);
});
for(const [name,event] of [
  ['child request',sentFrame({id:3,method:'thread/turns/list',params:{ref:sourceAnchor.sessionRef}},105)],
  ['replacement connection',{event:'Network.webSocketCreated',connection:'replacement',at:105}],
])test(`cached sample rejects ${name} arriving during DOM observation`,async()=>{
  const {trace,...parameters}=cachedRecovery();
  const evidence=await sampleCachedReloadRecovery(()=>trace.slice(),async()=>{
    trace.push(event);
    return parameters;
  },parameters);
  assert.equal(evidence.ready,false);
  assert.equal(evidence.wireUnchanged,false);
});
test('cached sample never attaches a reply received during observation to earlier geometry',async()=>{
  const {trace,...parameters}=cachedRecovery();
  const reply=trace.pop();
  const evidence=await sampleCachedReloadRecovery(()=>trace.slice(),async()=>{trace.push(reply);return parameters;},parameters);
  assert.equal(evidence.ready,false);
  assert.equal(evidence.pages[0].response,undefined);
  const next=await sampleCachedReloadRecovery(()=>trace.slice(),async()=>parameters,parameters);
  assert.equal(next.ready,true);
});
test('cached sample permits unrelated wire traffic during observation',async()=>{
  const {trace,...parameters}=cachedRecovery();
  const evidence=await sampleCachedReloadRecovery(()=>trace.slice(),async()=>{
    trace.push(sentFrame({id:3,method:'thread/read',params:{ref:'local:other',includeTurns:true}},105));
    return parameters;
  },parameters);
  assert.equal(evidence.ready,true);
  assert.equal(evidence.wireUnchanged,true);
});
for(const type of ['keydown','touchstart','touchmove','touchend'])test(`cached input latch retains ${type} after event detail overflow`,()=>{
  const document=new EventTarget();
  const window={__comicRoot:()=>null};
  runInNewContext(`(${recordReloadEvents.toString()})('local:root')`,{window,document,performance,sessionStorage:{getItem:()=>null}});
  document.dispatchEvent(new Event(type));
  for(let i=0;i<101;i++)document.dispatchEvent(new Event('scroll'));
  assert.equal(window.__comicReloadEvents.length,100);
  const evidence=cachedReloadRecovery({...cachedRecovery(),intent:window.__comicReloadEvents,inputSeen:window.__comicReloadInputSeen});
  assert.equal(evidence.ready,false);
});
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
const remoteTargets = {parentRef:'comic-remote:root', childRef:'comic-remote:child'};
const remoteFrame = (event, payload, connection='remote-document') => ({event:'Network.webSocketFrame'+event,connection,frame:JSON.stringify(payload)});
function remoteReads() {
  return [{event:'Network.webSocketCreated',connection:'remote-document'}, ...Object.values(remoteTargets).flatMap((ref,index)=>[
    remoteFrame('Sent',{id:index+1,method:'thread/read',params:{ref}}),
    remoteFrame('Received',{id:index+1,result:{thread:{id:ref.split(':')[1],evener:{ref}}}}),
  ])];
}
function liveDiscovery(ref='local:root', connection='remote-document') {
  return [
    remoteFrame('Sent',{id:10,method:'evener/navigation/read',params:{resource:'section',section:'live'}},connection),
    remoteFrame('Received',{id:10,result:{status:'ok',data:{entities:[{kind:'session',value:{ref,kind:'session',live:true}}]}}},connection),
  ];
}
test('remote reads coexist with previously discovered same-connection Live background owners',()=>{
  const rows=[...remoteReads(),...liveDiscovery(),remoteFrame('Sent',{id:11,method:'thread/read',params:{ref:'local:root'}})];
  assert.equal(verifyRemoteRequests(rows,remoteTargets).filter(request=>request.method==='thread/read').length,3);
});
for(const defect of ['missing','late','other connection','failed','wrong request ID','non-Live','non-section','archived','delegate','wrong ref','child counterpart']) {
  test(`remote local read rejects ${defect} discovery`,()=>{
    const ref=defect==='child counterpart'?'local:child':'local:root';
    let discovery=liveDiscovery(ref,defect==='other connection'?'old-document':'remote-document');
    const request=JSON.parse(discovery[0].frame),reply=JSON.parse(discovery[1].frame);
    if(defect==='missing')discovery=[];
    if(defect==='failed')reply.error={message:'failed'};
    if(defect==='wrong request ID')reply.id=99;
    if(defect==='non-Live')request.params.section='recent';
    if(defect==='non-section')request.params.resource='location';
    if(defect==='archived')reply.result.data.entities[0].value.live=false;
    if(defect==='delegate')reply.result.data.entities[0].value.kind='delegate';
    if(defect==='wrong ref')reply.result.data.entities[0].value.ref='local:other';
    if(discovery.length){discovery[0].frame=JSON.stringify(request);discovery[1].frame=JSON.stringify(reply);}
    const read=remoteFrame('Sent',{id:11,method:'thread/read',params:{ref}});
    assert.throws(()=>verifyRemoteRequests([...remoteReads(),...(defect==='late'?[read,...discovery]:[...discovery,read])],remoteTargets));
  });
}
for(const target of ['parentRef','childRef'])for(const defect of ['missing reply','failed','wrong connection','wrong request ID','wrong qualified ref','wrong thread ID']) {
  test(`remote ${target} requires successful correlated identity, ${defect}`,()=>{
    const rows=remoteReads(),index=target==='parentRef'?2:4;
    const reply=JSON.parse(rows[index].frame);
    if(defect==='missing reply')rows.splice(index,1);
    else {
      if(defect==='failed')reply.error={message:'failed'};
      if(defect==='wrong connection')rows[index].connection='old-document';
      if(defect==='wrong request ID')reply.id=99;
      if(defect==='wrong qualified ref')reply.result.thread.evener.ref=remoteTargets[target].replace('comic-remote:','local:');
      if(defect==='wrong thread ID')reply.result.thread.id='other';
      rows[index].frame=JSON.stringify(reply);
    }
    assert.throws(()=>verifyRemoteRequests(rows,remoteTargets));
  });
}
test('remote oracle excludes preceding document reads but rejects local reads on its own connection', () => {
  const sent=(connection,ref)=>({event:'Network.webSocketFrameSent',connection,frame:JSON.stringify({method:'thread/read',params:{ref}})});
  const created={event:'Network.webSocketCreated',connection:'remote-document'};
  assert.equal(verifyRemoteRequests([sent('old-document','local:old'),...remoteReads()],remoteTargets).length,2);
  assert.throws(()=>verifyRemoteRequests([created,sent('remote-document','local:wrong')],remoteTargets));
});
for (const method of ['thread/read','turn/start','thread/resume']) {
  for (const connection of ['destination','reconnected']) {
    test(`remote oracle rejects ${method} on ${connection}`, () => {
      const rows=[...remoteReads(),...['destination','reconnected'].map(connection=>({event:'Network.webSocketCreated',connection}))];
      rows.push({event:'Network.webSocketFrameSent',connection,frame:JSON.stringify({method,params:{ref:method==='thread/read'?'local:wrong':'comic-remote:root'}})});
      assert.throws(()=>verifyRemoteRequests(rows,remoteTargets));
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
