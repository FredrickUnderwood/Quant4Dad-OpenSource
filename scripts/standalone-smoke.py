#!/usr/bin/env python3
"""Read-only HTTP deployment checks. Credentials and response bodies are not printed."""
import argparse
import http.cookiejar
import json
import pathlib
import sys
import urllib.error
import urllib.request

EXPECTED_TOOLS = set('''list_instruments get_instrument query_kline read_kline_file analyze_kline
execute_python latest_bar_date get_data_coverage list_news get_news list_events get_event
list_indicators list_strategies get_strategy validate_strategy create_strategy update_strategy
list_cost_models run_backtest list_backtests get_backtest_job get_backtest_report
get_pipeline_node_types list_pipelines get_pipeline validate_pipeline create_pipeline
update_pipeline dry_run_pipeline_safe set_pipeline_status'''.split())

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, newurl):
        return None

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--web-url', required=True)
    parser.add_argument('--token-file', required=True)
    parser.add_argument('--mcp-url')
    parser.add_argument('--mcp-token-file')
    parser.add_argument('--expect-agent', action='store_true')
    args = parser.parse_args()
    evidence = {'checks': [], 'ok': False}
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    def request(name, url, expected=200, method='GET', value=None, headers=None):
        data = None if value is None else json.dumps(value).encode()
        h = dict(headers or {})
        if value is not None:
            h['Content-Type'] = 'application/json'
        req = urllib.request.Request(url, data=data, headers=h, method=method)
        try:
            result = opener.open(req, timeout=35)
        except urllib.error.HTTPError as error:
            result = error
        with result:
            body = result.read(2*1024*1024)
            code, response_headers = result.status, result.headers
        evidence['checks'].append({'name': name, 'http_status': code, 'ok': code == expected})
        if code != expected:
            raise ValueError(name)
        return body, response_headers
    base = args.web_url.rstrip('/')
    try:
        token = pathlib.Path(args.token_file).read_text().strip()
        if len(token) < 32:
            raise ValueError('login token file')
        body, _ = request('web SPA', base+'/')
        if b'<html' not in body.lower():
            raise ValueError('web SPA content')
        request('web readiness', base+'/readyz')
        request('authentication required', base+'/api/v1/instruments', 401)
        request('login', base+'/api/v1/login', method='POST', value={'token': token})
        status, _ = request('authenticated session', base+'/api/v1/auth/status')
        if not json.loads(status).get('authenticated'):
            raise ValueError('authenticated session')
        for path in ('instruments', 'strategies', 'costs', 'backtests', 'pipelines', 'pipeline/node-types', 'news', 'events'):
            request('API '+path, base+'/api/v1/'+path)
        request('Agent model surface', base+'/api/v1/agent/models', 200 if args.expect_agent else 404)
        if args.expect_agent:
            request('Agent session surface', base+'/api/v1/agent/sessions')
        if args.mcp_url:
            if not args.mcp_token_file:
                raise ValueError('MCP token file required')
            mcp_token = pathlib.Path(args.mcp_token_file).read_text().strip()
            request('MCP authentication', args.mcp_url, 401, 'POST', {'jsonrpc':'2.0','id':1,'method':'initialize','params':{}})
            headers = {'Authorization':'Bearer '+mcp_token, 'Accept':'application/json'}
            body, returned = request('MCP initialize', args.mcp_url, method='POST', value={'jsonrpc':'2.0','id':1,'method':'initialize','params':{'protocolVersion':'2025-03-26','capabilities':{},'clientInfo':{'name':'standalone-smoke','version':'1'}}}, headers=headers)
            headers['Mcp-Session-Id'] = returned.get('Mcp-Session-Id', '')
            if not headers['Mcp-Session-Id'] or 'error' in json.loads(body):
                raise ValueError('MCP initialization')
            body, _ = request('MCP catalog', args.mcp_url, method='POST', value={'jsonrpc':'2.0','id':2,'method':'tools/list','params':{}}, headers=headers)
            names = {tool['name'] for tool in json.loads(body)['result']['tools']}
            evidence['mcp_tool_count'] = len(names)
            if names != EXPECTED_TOOLS:
                raise ValueError('MCP catalog differs from 31 Agent tools')
            body, _ = request('MCP read tool', args.mcp_url, method='POST', value={'jsonrpc':'2.0','id':3,'method':'tools/call','params':{'name':'list_instruments','arguments':{'size':1}}}, headers=headers)
            payload = json.loads(body)
            if 'error' in payload or payload.get('result',{}).get('isError'):
                raise ValueError('MCP read tool failed')
            request('MCP close session', args.mcp_url, 200, 'DELETE', headers=headers)
        request('logout', base+'/api/v1/logout', method='POST')
        request('logout revoked cookie', base+'/api/v1/instruments', 401)
        evidence['ok'] = True
    except Exception as error:
        # Error strings can contain URLs or response data: record only their class.
        evidence['error_type'] = type(error).__name__
    print(json.dumps(evidence, ensure_ascii=False, indent=2))
    return 0 if evidence['ok'] else 1

if __name__ == '__main__':
    sys.exit(main())
