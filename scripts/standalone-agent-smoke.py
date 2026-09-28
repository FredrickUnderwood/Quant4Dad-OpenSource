#!/usr/bin/env python3
"""Run one real, read-only research conversation; report only bounded evidence.

The target must be a dedicated smoke installation with a configured model and
at least one imported instrument. No provider settings are changed by this runner.
"""
import argparse
import hashlib
import http.cookiejar
import json
import pathlib
import re
import secrets
import signal
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ID = re.compile(r'^[0-7][0-9A-HJKMNP-TV-Z]{25}$')
SYMBOL = re.compile(r'^(sh|sz|bj)\.[0-9]{6}$')
SAFE_CODE = re.compile(r'^[a-z][a-z0-9_]{0,95}$')
TERMINAL = {'completed', 'failed', 'interrupted', 'cancelled'}
PROMPT = '''这是部署验收用的只读研究任务。请只调用一次 list_instruments，参数必须为 {"size":1}（允许工具需要时补 page=1）。
根据这次工具真实返回的唯一一条记录，最后只回复一行：验证成功：<实际代码>。
不得猜测代码，不得执行任何其他工具，不得创建、修改或删除任何业务对象，不得回测、发送通知或请求审批。
若工具失败或没有数据，请明确报告失败并结束，不要重试。请保持回答简短。'''

class CheckFailure(Exception):
    pass

class HTTPFailure(CheckFailure):
    def __init__(self, name, status, payload):
        super().__init__(name+'_http_'+str(status))
        self.status = status
        self.payload = payload

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, newurl):
        return None

def require(condition, code):
    if not condition:
        raise CheckFailure(code)

def safe_code(value):
    return value if isinstance(value, str) and SAFE_CODE.fullmatch(value) else 'unspecified'

def read_events(body, run_id, session_id):
    events = []
    for frame in body.decode('utf-8').replace('\r\n', '\n').split('\n\n'):
        data = '\n'.join(line[5:].lstrip(' ') for line in frame.split('\n') if line.startswith('data:'))
        if not data:
            continue
        event = json.loads(data)
        require(event.get('run_id') == run_id and event.get('session_id') == session_id and event.get('schema_version') == 1, 'event_binding_invalid')
        require(isinstance(event.get('data'), dict), 'event_data_invalid')
        events.append(event)
    require(events and events[-1].get('type') in {'run.'+state for state in TERMINAL}, 'terminal_event_missing')
    return events

class Smoke:
    def __init__(self, args):
        parsed = urllib.parse.urlsplit(args.web_url)
        require(parsed.scheme in {'http', 'https'} and parsed.netloc and not parsed.username and not parsed.password and not parsed.query and not parsed.fragment, 'invalid_web_url')
        require(re.fullmatch(r'[A-Za-z0-9_.-]{1,128}', args.provider) and re.fullmatch(r'[A-Za-z0-9_./:-]{1,128}', args.model), 'invalid_model_identity')
        require(30 <= args.timeout <= 600, 'invalid_timeout')
        self.args = args
        self.base = args.web_url.rstrip('/')
        self.deadline = time.monotonic()+args.timeout
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.session = None
        self.run = None
        self.terminal = False
        self.attempt = 'standalone-smoke-'+secrets.token_hex(10)
        self.result = {'ok':False, 'attempt':self.attempt, 'provider':args.provider, 'model':args.model, 'profile':'research', 'checks':[], 'cleanup':{}, 'submission_count':0}

    def request(self, name, path, method='GET', data=None, expected=(200,), headers=None, record=True, cleanup=False, raw=False):
        remaining = 15 if cleanup else self.deadline-time.monotonic()
        require(remaining > 0, 'deadline_exceeded')
        require(path.startswith('/api/v1/'), 'unexpected_request_path')
        h = dict(headers or {})
        body = None if data is None else json.dumps(data).encode('utf-8')
        if body is not None:
            h['Content-Type'] = 'application/json'
        req = urllib.request.Request(self.base+path, body, h, method=method)
        try:
            response = self.opener.open(req, timeout=min(30,remaining))
        except urllib.error.HTTPError as error:
            response = error
        with response:
            content = response.read(2*1024*1024+1)
            status = response.status
        require(len(content) <= 2*1024*1024, 'response_too_large')
        payload = content if raw else json.loads(content) if content else {}
        if record:
            self.result['checks'].append({'name':name,'http_status':status,'ok':status in expected})
        if status not in expected:
            if isinstance(payload, dict):
                self.result['last_error_code'] = safe_code(payload.get('message'))
            raise HTTPFailure(name,status,payload)
        return payload

    def wait_session(self, created):
        self.session = created.get('session_id')
        require(isinstance(self.session,str) and ID.fullmatch(self.session), 'session_identity_invalid')
        self.result['session_id'] = self.session
        status = created.get('status')
        while status == 'provisioning':
            require(time.monotonic() < self.deadline, 'session_provision_timeout')
            time.sleep(1)
            value = self.request('session_metadata','/api/v1/agent/sessions/'+self.session+'/metadata',record=False)
            status = value.get('status')
        require(status == 'active', 'session_not_active')

    def execute(self):
        token = pathlib.Path(self.args.token_file).read_text().strip()
        require(len(token) >= 32, 'login_token_invalid')
        self.request('login','/api/v1/login','POST',{'token':token})
        options = self.request('research_profile','/api/v1/agent/options')
        require('research' in options.get('profiles',[]) and options.get('text_only') is False, 'research_tools_unavailable')
        market = self.request('instrument_fixture','/api/v1/instruments?size=1')
        items = market.get('items')
        require(isinstance(items,list) and len(items)==1 and SYMBOL.fullmatch(items[0].get('code','')), 'import_instrument_fixture_first')
        expected_symbol = items[0]['code']
        models = self.request('model_catalog','/api/v1/agent/models')
        matches = [m for m in models.get('models',[]) if m.get('provider')==self.args.provider and m.get('model')==self.args.model]
        require(len(matches)==1, 'requested_model_missing')
        self.result['probe_performed'] = False
        if matches[0].get('status') != 'ready':
            probe = self.request('model_probe','/api/v1/agent/models/'+urllib.parse.quote(self.args.provider,safe='')+'/probe','POST',{'force':False})
            self.result['probe_performed'] = True
            self.result['probe'] = {key:probe.get(key) for key in ('status','reason','model_turns','tool_calls','first_event_ms')}
            require(probe.get('status')=='ready', 'model_probe_failed')
            models = self.request('model_ready','/api/v1/agent/models')
            require(any(m.get('provider')==self.args.provider and m.get('model')==self.args.model and m.get('status')=='ready' for m in models.get('models',[])), 'model_not_ready')
        # Synchronization is a read-only poll, never another model probe.
        while True:
            status = self.request('runtime_status','/api/v1/agent/status',record=False)
            state = status.get('status')
            if state=='ready':
                break
            require(state=='synchronizing', 'runtime_'+safe_code(state))
            require(time.monotonic()<self.deadline, 'runtime_sync_timeout')
            time.sleep(1)
        require(status.get('profiles_aligned') is not False, 'runtime_profiles_misaligned')
        created = self.request('create_session','/api/v1/agent/sessions','POST',{'provider':self.args.provider,'model':self.args.model,'profile':'research','title':self.attempt},expected=(201,202),headers={'Idempotency-Key':self.attempt})
        self.wait_session(created)
        self.result['submission_count'] = 1
        try:
            accepted = self.request('submit_run','/api/v1/agent/sessions/'+self.session+'/messages','POST',{'client_request_id':self.attempt,'content':[{'type':'text','text':PROMPT}]},expected=(202,))
        except HTTPFailure as error:
            # A failed acknowledgement can still identify an owned durable grant.
            # Capture it for cancellation; never resubmit the model request.
            candidate = error.payload.get('run_id') if isinstance(error.payload,dict) else None
            if isinstance(candidate,str) and ID.fullmatch(candidate):
                self.run = candidate
                self.result['run_id'] = candidate
            raise
        self.run = accepted.get('run_id')
        require(isinstance(self.run,str) and ID.fullmatch(self.run), 'run_identity_invalid')
        self.result['run_id'] = self.run
        self.result['states'] = []
        while True:
            require(time.monotonic()<self.deadline, 'run_timeout')
            snapshot = self.request('run_status','/api/v1/agent/runs/'+self.run,record=False)
            require(snapshot.get('session_id')==self.session and snapshot.get('run_id')==self.run, 'run_binding_invalid')
            state = snapshot.get('state')
            if not self.result['states'] or self.result['states'][-1] != state:
                self.result['states'].append(safe_code(state))
            require(state != 'waiting_approval', 'unexpected_approval_request')
            if snapshot.get('terminal'):
                self.terminal = True
                require(state in TERMINAL, 'unknown_terminal_state')
                break
            time.sleep(1)
        wire = self.request('replay_events','/api/v1/agent/runs/'+self.run+'/events',headers={'Accept':'text/event-stream','Last-Event-ID':'0'},raw=True)
        events = read_events(wire,self.run,self.session)
        self.result['terminal_state'] = state
        self.result['event_count'] = len(events)
        self.result['event_types'] = sorted({e['type'] for e in events})
        self.result['run_usage'] = {key:sum(e['data'].get('usage',{}).get(key,0) for e in events if e['type']=='usage.updated') for key in ('input_tokens','output_tokens')}
        failures = [e for e in events if e['type']=='run.failed']
        if failures:
            self.result['run_error_code'] = safe_code(failures[-1]['data'].get('code'))
        require(state=='completed' and events[-1]['type']=='run.completed', 'run_did_not_complete')
        self.verify_result(events,expected_symbol)
        self.result['ok'] = True

    def verify_result(self, events, expected_symbol):
        proposed = [e['data'] for e in events if e['type']=='tool.proposed']
        require(len(proposed)==1, 'expected_exactly_one_tool_call')
        call = proposed[0]
        name = call.get('name','').removeprefix('mcp__q4d__')
        require(name=='list_instruments' and not call.get('arguments_omitted'), 'unexpected_tool')
        require(call.get('arguments') in ({'size':1},{'page':1,'size':1}), 'unexpected_tool_arguments')
        call_id = call.get('tool_call_id')
        require(isinstance(call_id,str) and ID.fullmatch(call_id), 'tool_identity_invalid')
        require(any(e['type']=='tool.completed' and e['data'].get('tool_call_id')==call_id for e in events), 'tool_completion_missing')
        require(not any(e['type'] in {'tool.failed','approval.required'} for e in events), 'tool_failed_or_requested_approval')
        audit = self.request('internal_tool_audit','/api/v1/agent/runs/'+self.run+'/tools/'+call_id)
        require(audit.get('status')=='succeeded' and audit.get('risk')=='R0' and audit.get('tool_name')=='list_instruments' and audit.get('run_id')==self.run, 'internal_tool_audit_invalid')
        payload = audit.get('result',{})
        require(payload.get('untrusted_data') is True, 'tool_data_envelope_invalid')
        data = payload.get('data',{})
        rows = data.get('items',[])
        require(data.get('size')==1 and data.get('count')==1 and len(rows)==1 and rows[0].get('code')==expected_symbol, 'tool_result_does_not_match_database')
        detail = self.request('conversation_transcript','/api/v1/agent/sessions/'+self.session+'?limit=100')
        transcript = detail.get('transcript') or {}
        require(transcript.get('session_id')==self.session and transcript.get('has_more') is False, 'transcript_incomplete')
        messages = [item for item in transcript.get('items',[]) if item.get('run_id')==self.run]
        assistants = [item for item in messages if item.get('role')=='assistant' and any(block.get('type')=='text' for block in item.get('content',[]))]
        require(assistants, 'final_answer_missing')
        last = assistants[-1]
        require(not any(block.get('type')=='tool_request' for block in last.get('content',[])), 'final_answer_contains_tool_request')
        final = '\n'.join(block['text'] for block in last.get('content',[]) if block.get('type')=='text')
        require(expected_symbol in final, 'final_answer_does_not_reference_result')
        self.result['verified_tool'] = {'name':name,'size':1,'risk':'R0','status':'succeeded','symbol':expected_symbol}
        self.result['final_answer'] = {'references_actual_symbol':True,'characters':len(final),'sha256':hashlib.sha256(final.encode()).hexdigest()}

    def cleanup(self):
        if self.run and not self.terminal:
            try:
                cancelled = self.request('cancel_owned_run','/api/v1/agent/runs/'+self.run+'/cancel','POST',{},cleanup=True)
                self.result['cleanup']['cancel_requested'] = True
                deadline = time.monotonic()+10
                while not cancelled.get('terminal') and time.monotonic()<deadline:
                    time.sleep(1)
                    cancelled = self.request('cancel_status','/api/v1/agent/runs/'+self.run,cleanup=True,record=False)
                self.result['cleanup']['run_terminal'] = bool(cancelled.get('terminal'))
                if not cancelled.get('terminal'):
                    self.result['cleanup']['error'] = 'cancellation_not_confirmed'
            except Exception:
                self.result['cleanup']['error'] = 'cancellation_failed'
        if self.session:
            try:
                archived = self.request('archive_owned_session','/api/v1/agent/sessions/'+self.session,'PATCH',{'archived':True},cleanup=True)
                check = self.request('verify_archived_session','/api/v1/agent/sessions/'+self.session+'/metadata',cleanup=True,record=False)
                self.result['cleanup']['session_archived'] = archived.get('status')=='archived' and check.get('status')=='archived'
                if not self.result['cleanup']['session_archived']:
                    self.result['cleanup']['error'] = 'archive_not_confirmed'
            except Exception:
                self.result['cleanup']['error'] = 'archive_failed'
        if self.result['cleanup'].get('error'):
            self.result['ok'] = False

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--web-url',required=True)
    parser.add_argument('--token-file',required=True)
    parser.add_argument('--provider',required=True)
    parser.add_argument('--model',required=True)
    parser.add_argument('--timeout',type=int,default=180)
    args = parser.parse_args()
    smoke = None
    def interrupted(signum, frame):
        raise CheckFailure('interrupted')
    signal.signal(signal.SIGTERM,interrupted)
    signal.signal(signal.SIGINT,interrupted)
    try:
        smoke = Smoke(args)
        smoke.execute()
    except Exception as error:
        result = smoke.result if smoke else {'ok':False}
        result['error'] = str(error) if isinstance(error,CheckFailure) else type(error).__name__
    finally:
        if smoke:
            smoke.cleanup()
            result = smoke.result
    print(json.dumps(result,ensure_ascii=False,indent=2))
    return 0 if result['ok'] else 1

if __name__=='__main__':
    sys.exit(main())
