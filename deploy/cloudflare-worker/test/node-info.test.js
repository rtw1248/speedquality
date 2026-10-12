import assert from 'node:assert/strict';
import test from 'node:test';
import worker from '../src/index.js';
import { prepareNodeInfo, verifyReportNodeInfo, nodeDescription } from '../src/node-info.js';
import { sanitizeCoverage } from '../src/node-coverage.js';

const env = { NODE_CORE_SECRET: 'metadata-test-secret-long-enough' };
const candidate = () => ({ id:'a'.repeat(32), node_info:{city:'武汉',city_status:'reported',source:'third_party'} });
const lease = () => ({region:{code:'hb'},family:'v4',targets:[{carrier:'ct',candidates:[candidate()]}]});
const request = new Request('https://sq.example/api/node-lease',{headers:{'X-SQ-Node-Info':'1'}});

test('metadata capability preserves strict old probes and signs only valid cities',async()=>{
  const old=lease();
  await prepareNodeInfo(old,new Request(request.url),env,'session-a');
  assert.equal(old.targets[0].candidates[0].node_info,undefined);
  const invalid=lease(); invalid.targets[0].candidates[0].node_info.city='武汉\x1b[31m';
  await prepareNodeInfo(invalid,request,env,'session-a');
  assert.equal(invalid.targets[0].candidates[0].node_info,undefined);
});

test('signed node snapshot cannot be moved between sessions, families, carriers or nodes',async()=>{
  const value=lease(); await prepareNodeInfo(value,request,env,'session-a');
  const reports=()=>[{region:{code:'hb'},family:'v4',results:[{carrier:'ct',node_id:'a'.repeat(32),node_used:true,
    node_info:{...value.targets[0].candidates[0].node_info}}]}];
  const valid=reports(); await verifyReportNodeInfo(valid,env,'session-a');
  assert.equal(valid[0].results[0].node_info.city,'武汉');
  assert.equal(valid[0].results[0].node_info.proof,undefined);
  for (const mutate of [r=>r[0].family='v6',r=>r[0].region.code='bj',r=>r[0].results[0].carrier='cu',
    r=>r[0].results[0].node_id='b'.repeat(32),r=>r[0].results[0].node_info.city='北京',r=>r[0].results[0].node_used=false]) {
    const changed=reports(); mutate(changed); await verifyReportNodeInfo(changed,env,'session-a');
    assert.equal(changed[0].results[0].node_info,undefined);
  }
  const foreign=reports(); await verifyReportNodeInfo(foreign,env,'session-b');
  assert.equal(foreign[0].results[0].node_info,undefined);
  const paired=reports(); await verifyReportNodeInfo(paired,env,['session-b','session-a']);
  assert.equal(paired[0].results[0].node_info.city,'武汉');
  assert.equal(nodeDescription({node_id:'a'.repeat(32),node_used:false}), '');
  assert.equal(nodeDescription({node_id:'a'.repeat(32),single:{download_mbps:100,upload_mbps:100}}), '');
  assert.equal(nodeDescription({node_id:'a'.repeat(32),node_used:true,
    node_info:{city:'',city_status:'unknown',source:'third_party'}}),'aaaaaaaaaaaa · 第三方');
});

test('coverage whitelists public fields and hides unavailable data without inventing empty coverage',async()=>{
  const data={version:1,generated_at:Math.floor(Date.now()/1000),nodes:[{id:'a'.repeat(32),region:'hb',carrier:'edu',
    family:'v6',city:'武汉',city_status:'verified',source:'community',status:'verified',address:'192.0.2.1',route_key:'secret'}]};
  const clean=sanitizeCoverage(data);
  assert.equal(clean.education_public_testing,false);
  assert.equal(clean.nodes[0].public_testing,false);
  assert.doesNotMatch(JSON.stringify(clean),/secret|192\.0\.2\.1|route_key|address/);
  const pending = structuredClone(data);
  Object.assign(pending.nodes[0], {status:'pending',pending_reason:'verification_pending'});
  assert.equal(sanitizeCoverage(pending).nodes[0].pending_reason,'verification_pending');
  pending.nodes[0].pending_reason='internal address 192.0.2.1';
  assert.equal(sanitizeCoverage(pending).nodes[0].pending_reason,undefined);
  const publicEnv={NODE_CORE_SECRET:'coverage-secret',NODE_CORE:{fetch:async request=>{
    assert.equal(request.method,'GET'); assert.equal(new URL(request.url).pathname,'/coverage');
    assert.equal(request.headers.get('x-node-core-secret'),'coverage-secret');
    assert.equal(request.headers.has('cf-connecting-ip'),false); return Response.json(data);
  }}};
  const response=await worker.fetch(new Request('https://sq.example/api/nodes/coverage'),publicEnv);
  assert.equal(response.status,200); assert.match(response.headers.get('cache-control'),/s-maxage=300/);
  assert.deepEqual(await response.json(),clean);
  const unavailable=await worker.fetch(new Request('https://sq.example/api/nodes/coverage'),{});
  assert.equal(unavailable.status,503); assert.equal(unavailable.headers.get('cache-control'),'no-store');
  const page=await worker.fetch(new Request('https://sq.example/nodes'),{});
  const html=await page.text();
  assert.equal(page.status,200); assert.match(page.headers.get('content-security-policy'),/connect-src 'self'/);
  assert.match(html,/教育网建设中，暂未开放公共测速/); assert.doesNotMatch(html,/SQ_MAP_PREVIEW|演示数据|demo-hb/);
  assert.equal((html.match(/class="province"/g)||[]).length,34);
});
