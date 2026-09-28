import test, { before, after } from 'node:test'
import assert from 'node:assert/strict'
import { spawn, execFile } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdtemp, rm } from 'node:fs/promises'
import { createInterface } from 'node:readline'
import { once } from 'node:events'
import { promisify } from 'node:util'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js'
import { TrustedGateway } from '../../../src/mcp/trusted-gateway.mjs'

const root=resolve(import.meta.dirname,'../../../..'), token='fixture-internal-mcp-token-0123456789'
const callID='01K00000000000000000000000'
let directory,binary
before(async()=>{
 directory=await mkdtemp(join(tmpdir(),'q4d-internal-mcp-'));binary=join(directory,'gateway.test')
 await promisify(execFile)('go',['test','-mod=readonly','-c','-tags=mcpintegration','-o',binary,'./internal/mcp'],{cwd:root,timeout:120000})
},{timeout:125000})
after(async()=>{if(directory)await rm(directory,{recursive:true,force:true})})
async function fixture(t){
 const child=spawn(binary,['-test.run=^TestInternalMCPControlHost$','-test.timeout=35s'],{env:{PATH:process.env.PATH,Q4D_MCP_FIXTURE:'1'},stdio:['pipe','pipe','pipe']})
 const ready=Promise.withResolvers(),exited=once(child,'exit'),lines=createInterface({input:child.stdout}),pending=new Map()
 let logs='',nextID=0
 lines.on('line',line=>{if(line.startsWith('Q4D_GATEWAY\t')){let value;try{value=JSON.parse(line.slice(12))}catch{const error=new Error('invalid synthetic fixture line: '+JSON.stringify(line));ready.reject(error);for(const p of pending.values())p.reject(error);return}if(value.ready)ready.resolve(value);else{pending.get(value.id)?.resolve(value);pending.delete(value.id)}}else logs+=line+'\n'})
 child.stderr.on('data',bytes=>{logs+=bytes});child.on('error',ready.reject);child.on('exit',()=>{ready.reject(new Error(logs));for(const value of pending.values())value.reject(new Error(logs))})
 t.after(async()=>{child.stdin.end();const timer=setTimeout(()=>child.kill('SIGKILL'),3000);const [code]=await exited.finally(()=>clearTimeout(timer));lines.close();assert.equal(code,0,logs)})
 const value=await ready.promise
 const control=(op,argumentsText)=>{const id=++nextID,p=Promise.withResolvers();pending.set(id,p);child.stdin.write(JSON.stringify({id,op,...(argumentsText===undefined?{}:{arguments:argumentsText})})+'\n');return p.promise}
 return {...value,control}
}
test('TEST-INTERNAL-MCP-01 signed SDK discovery and original result replay use the actual Go ledger',{timeout:35000},async t=>{
 const f=await fixture(t),headers={authorization:'Bearer '+token,'X-Q4D-Run-Capability':f.capability,'X-Q4D-Tool-Call-ID':callID,'Idempotency-Key':`q4d:${f.claims.envelope.run_id}:${callID}`}
 const client=new Client({name:'q4d-internal-fixture',version:'1'});t.after(()=>client.close())
 await client.connect(new StreamableHTTPClientTransport(new URL(f.url),{requestInit:{headers}}))
 const tools=(await client.listTools()).tools;assert.equal(tools.length,4);assert.ok(tools.every(x=>x.annotations.readOnlyHint))
 const started=[]
 const call=()=>client.callTool({name:'query_kline',arguments:{code:'sh.600519',limit:2}},undefined,{onprogress:p=>started.push(p)})
 const first=await call();assert.equal(first.isError,false);assert.equal(first.structuredContent.data.bars.at(-1).close,599)
 await f.control('change-bars');assert.deepEqual(await call(),first)
 assert.equal(started.length,2);assert.ok(started.every(p=>p.progress===0&&p.total===1&&p.message==='q4d.tool.started.v1'))
 const changed=await client.callTool({name:'query_kline',arguments:{code:'sh.600519',limit:3}},undefined,{onprogress:()=>assert.fail('conflict started')})
 assert.equal(changed.isError,true);assert.equal(changed.content[0].text,'agent_tool_call_conflict')
 const status=await f.control('status');assert.equal(status.queries,1);assert.equal(status.audits.length,1);assert.equal(status.audits[0].Status,'succeeded');assert.match(status.audits[0].ArgsHash,/^sha256:[0-9a-f]{64}$/)
 assert.ok(!JSON.stringify(status.audits).includes(f.capability))
})
test('TEST-INTERNAL-MCP-02 trusted Runtime transport receives Go start marker and revocation prevents replay',{timeout:35000},async t=>{
 const f=await fixture(t),gateway=new TrustedGateway({url:f.url,runtimeToken:token,catalog:f.catalog,maxResponseBytes:512*1024})
 const session=f.claims.session_id
 gateway.beginRun(session,{sessionId:session,runId:f.claims.envelope.run_id,capability:f.capability,expiresAt:f.claims.exp*1000,allowedTools:f.claims.allowed_tools,authorize:()=>true})
 const invocation=gateway.prepare(session,'query_kline',{code:'sh.600519',limit:1});let starts=0
 const result=await gateway.invoke(invocation,{onStarted:()=>starts++});assert.equal(result.isError,false);assert.equal(starts,1)
 await f.control('revoke')
 await assert.rejects(gateway.invoke(invocation,{onStarted:()=>assert.fail('revoked call started')}))
 assert.equal((await f.control('status')).queries,1)
 gateway.endRun(session,f.claims.envelope.run_id)
 await assert.rejects(gateway.invoke(invocation))
})
test('TEST-INTERNAL-MCP-03 Go canonical bytes and hashes match ECMAScript for bounded objects with nullable fields',{timeout:35000},async t=>{
 const f=await fixture(t)
 const canonical=value=>Array.isArray(value)?'['+value.map(canonical).join(',')+']':value!==null&&typeof value==='object'?'{'+Object.keys(value).sort().map(key=>JSON.stringify(key)+':'+canonical(value[key])).join(',')+'}':JSON.stringify(value)
 const cases=[{'\ue000':1,'😀':2,keyword:'中文 <>&\u2028\u2029',array:[true,false,-0,1e-7,1e-6,1e20,1e21,1e23,Number.MAX_VALUE,Number.MIN_VALUE]}, {code:'sh.600519',limit:2}]
 for(let i=1;i<=100;i++)cases.push({n:Math.sin(i)*10**((i%50)-25),nested:{b:i,a:'e\u0301'}})
 for(const value of [...cases,{n:null,nested:[null,{empty:null}]}]){const got=await f.control('canonical',JSON.stringify(value)),want=canonical(value);assert.equal(Buffer.from(got.canonical_base64,'base64').toString('utf8'),want);assert.equal(got.hash,'sha256:'+createHash('sha256').update(want).digest('hex'))}
 for(const raw of ['{"a":1,"\\u0061":2}','{"s":"\\ud800"}','null','{"n":1e999}'])assert.equal((await f.control('canonical',raw)).error,'invalid')
 assert.equal((await f.control('status')).queries,0)
})
