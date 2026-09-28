#!/usr/bin/env python3
"""Exercise the complete deployed Agent MCP catalog with isolated smoke records.

Usage: python3 scripts/mcp-live-test.py --url https://host/mcp --token-file /path/to/token
Only newly created MCP Smoke strategies/pipelines are mutated. The pipeline has
one keyword filter, no subscriptions, no model node and no delivery node. Its
enabled state is always followed by a disable attempt. Tokens and session IDs
are never written into the JSON evidence.
"""

import argparse
import copy
import datetime as dt
import json
import pathlib
import re
import secrets
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


EXPECTED = set("""
list_instruments get_instrument query_kline read_kline_file analyze_kline
execute_python latest_bar_date get_data_coverage list_news get_news list_events
get_event list_indicators list_strategies get_strategy validate_strategy
create_strategy update_strategy list_cost_models run_backtest list_backtests
get_backtest_job get_backtest_report get_pipeline_node_types list_pipelines
get_pipeline validate_pipeline create_pipeline update_pipeline dry_run_pipeline_safe
set_pipeline_status
""".split())


class CheckFailure(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def require(value, message):
    if not value:
        raise CheckFailure(message)


class Smoke:
    def __init__(self, url, token):
        self.url = url
        self.token = token
        self.session = ""
        self.sequence = 0
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        self.evidence = {
            "started_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "endpoint": url,
            "expected_tool_count": len(EXPECTED),
            "checks": [],
            "tools": {},
            "created": {},
        }

    def record(self, name, ok, **details):
        self.evidence["checks"].append({"name": name, "ok": bool(ok), **details})

    def http(self, method="POST", body=None, *, session=None, auth=True, headers=None):
        head = {"Accept": "application/json, text/event-stream"}
        if auth:
            head["Authorization"] = "Bearer " + self.token
        sid = self.session if session is None else session
        if sid:
            head["Mcp-Session-Id"] = sid
            head["Mcp-Protocol-Version"] = "2025-11-25"
        if headers:
            head.update(headers)
        payload = None
        if body is not None:
            payload = json.dumps(body, ensure_ascii=False, separators=(",", ":")).encode()
            head["Content-Type"] = "application/json"
        request = urllib.request.Request(self.url, data=payload, headers=head, method=method)
        try:
            response = self.opener.open(request, timeout=45)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read(1024 * 1024 + 1)
            require(len(data) <= 1024 * 1024, "response_exceeded_limit")
            return response.code, response.headers, data

    def rpc(self, method, params=None, *, request_id=None, session=None):
        self.sequence += 1
        rid = request_id if request_id is not None else "smoke-" + str(self.sequence)
        body = {"jsonrpc": "2.0", "id": rid, "method": method}
        if params is not None:
            body["params"] = params
        status, headers, raw = self.http(body=body, session=session)
        require(status == 200, "rpc_http_" + str(status))
        if headers.get_content_type() == "text/event-stream":
            frames = [line[6:] for line in raw.decode().splitlines() if line.startswith("data: ")]
            replies = [json.loads(frame) for frame in frames if frame.strip()]
            replies = [reply for reply in replies if reply.get("id") == rid]
            require(len(replies) == 1, "invalid_sse_response")
            reply = replies[0]
        else:
            require(headers.get_content_type() == "application/json", "invalid_response_type")
            reply = json.loads(raw)
        require(reply.get("jsonrpc") == "2.0" and reply.get("id") == rid, "rpc_identity_mismatch")
        require("error" not in reply, "rpc_error_" + str(reply.get("error", {}).get("code", "unknown")))
        return reply.get("result"), headers

    def initialize(self):
        result, headers = self.rpc("initialize", {
            "protocolVersion": "2025-11-25", "capabilities": {},
            "clientInfo": {"name": "quant4dad-mcp-smoke", "version": "1"},
        }, session="")
        require(result.get("protocolVersion") == "2025-11-25", "protocol_negotiation_failed")
        sid = headers.get("Mcp-Session-Id", "")
        require(bool(sid), "initialize_missing_session")
        return sid

    def tool(self, name, arguments=None, *, request_id=None, session=None, expect_error=False):
        started = time.monotonic()
        result, _ = self.rpc("tools/call", {"name": name, "arguments": arguments or {}},
                             request_id=request_id, session=session)
        is_error = result.get("isError", False)
        code = ""
        if is_error:
            text = " ".join(item.get("text", "") for item in result.get("content", []) if item.get("type") == "text")
            code = text if re.fullmatch(r"[a-zA-Z0-9_]+", text) else "redacted_tool_error"
        ok = bool(is_error) == expect_error
        stats = self.evidence["tools"].setdefault(name, {"calls": 0, "successes": 0, "expected_errors": 0, "failures": 0})
        stats["calls"] += 1
        stats["expected_errors" if ok and expect_error else "successes" if ok else "failures"] += 1
        self.record("tool:" + name, ok, duration_ms=round((time.monotonic() - started) * 1000),
                    **({"code": code} if code else {}), **({"expected_error": True} if expect_error else {}))
        require(ok, name + ":" + (code or "expected_error_missing"))
        if expect_error:
            return code
        value = result.get("structuredContent")
        if value is None:
            texts = [item.get("text", "") for item in result.get("content", []) if item.get("type") == "text"]
            value = json.loads(texts[0]) if texts else None
        require(isinstance(value, dict) and value.get("untrusted_data") is True and "data" in value,
                name + ":result_envelope_missing")
        return value["data"]

    def phase(self, name, function):
        try:
            function()
        except (CheckFailure, urllib.error.URLError, TimeoutError, ValueError, KeyError, TypeError, OSError) as error:
            message = str(error) if isinstance(error, CheckFailure) else type(error).__name__
            self.record("phase:" + name, False, error=message)


def run(smoke, args):
    ping = {"jsonrpc": "2.0", "id": "auth-probe", "method": "ping"}
    status, _, _ = smoke.http(body=ping, auth=False, session="")
    require(status == 401, "unauthenticated_access_not_denied")
    smoke.record("unauthenticated_denied", True, http_status=status)
    smoke.session = smoke.initialize()
    smoke.record("initialize_session", True)
    status, _, body = smoke.http(body={"jsonrpc": "2.0", "method": "notifications/initialized"})
    require(status == 202 and not body, "initialized_notification_rejected")
    smoke.record("initialized_notification", True)
    for label, sid, expected in [("missing_session", "", 400), ("unknown_session", "unknown", 404)]:
        status, _, _ = smoke.http(body=ping, session=sid)
        require(status == expected, label + "_not_rejected")
        smoke.record(label, True, http_status=status)
    for key in ["X-Q4D-Run-Capability", "X-Q4D-Approval-Receipt", "Idempotency-Key"]:
        status, _, _ = smoke.http(body=ping, headers={key: "forged"})
        require(status == 400, "internal_authority_accepted")
        smoke.record("reject_header:" + key, True)
    status, _, _ = smoke.http(body=ping, headers={"Origin": "https://invalid.example"})
    require(status in (400, 403), "origin_accepted")
    smoke.record("origin_denied", True, http_status=status)
    status, _, _ = smoke.http(method="GET")
    require(status == 405, "get_stream_capability_mismatch")
    result, _ = smoke.rpc("tools/list")
    definitions = result.get("tools", [])
    names = {definition["name"] for definition in definitions}
    smoke.evidence["catalog"] = {"count": len(definitions), "names": sorted(names),
                                  "missing": sorted(EXPECTED - names), "unexpected": sorted(names - EXPECTED)}
    require(names == EXPECTED and len(definitions) == len(EXPECTED), "catalog_does_not_match_31_agent_tools")
    require(not any(d.get("_meta", {}).get("q4d/deprecated") for d in definitions), "deprecated_tool_advertised")
    smoke.record("agent_catalog_exact_coverage", True)
    context = {}
    unique = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%d-%H%M%S-") + secrets.token_hex(3)
    prefix = "MCP Smoke " + unique

    def market():
        instruments = smoke.tool("list_instruments", {"keyword": args.code.split(".")[-1], "size": 10})
        matches = [item for item in instruments.get("items", []) if item.get("code") == args.code]
        require(matches, "requested_instrument_missing_from_catalog")
        context["code"] = matches[0]["code"]
        smoke.tool("get_instrument", {"code": context["code"]})
        smoke.tool("latest_bar_date", {"code": context["code"]})
        smoke.tool("get_data_coverage", {"code": context["code"]})
        query_args = {"code": context["code"], "limit": 30}
        descriptor = smoke.tool("query_kline", query_args, request_id="stable-kline")
        require(descriptor.get("count", 0) > 1 and descriptor.get("file_id"), "insufficient_local_bars")
        require(smoke.tool("query_kline", query_args, request_id="stable-kline") == descriptor,
                "read_replay_changed_result")
        smoke.record("read_idempotent_replay", True)
        context["file"] = descriptor
        smoke.tool("read_kline_file", {"file_id": descriptor["file_id"], "limit": 5})
        smoke.tool("analyze_kline", {"file_id": descriptor["file_id"], "threshold_pct": 0})
        python = smoke.tool("execute_python", {"file_ids": [descriptor["file_id"]], "title": prefix,
            "code": "import json\ndata=json.load(open('/data/input.json'))\ncount=sum(len(item['rows']) for item in data['datasets'])\nprint(json.dumps({'summary':'MCP isolated runtime smoke','metrics':{'count':count}},allow_nan=False))"})
        require(python.get("status") == "succeeded", "python_runtime_not_succeeded")
        require(python.get("result", {}).get("metrics", {}).get("count") == descriptor["count"], "python_count_mismatch")
        smoke.record("isolated_python_runtime", True, input_count=descriptor["count"])
        other = smoke.initialize()
        try:
            smoke.tool("read_kline_file", {"file_id": descriptor["file_id"]}, session=other, expect_error=True)
            smoke.record("cross_session_file_denied", True)
        finally:
            status, _, _ = smoke.http(method="DELETE", session=other)
            smoke.record("secondary_session_closed", status == 200)
        status, _, body = smoke.http(body={"jsonrpc": "2.0", "method": "tools/call", "params": {
            "name": "query_kline", "arguments": query_args}})
        require(status == 202 and not body, "tool_notification_failed")
        smoke.record("tool_notification_accepted_without_result", True)

    def queries():
        for name in ["list_strategies", "list_cost_models", "list_backtests", "list_news", "list_pipelines"]:
            context[name] = smoke.tool(name, {"limit": 100 if name == "list_pipelines" else 10})
        smoke.tool("list_indicators")
        context["node_types"] = smoke.tool("get_pipeline_node_types")
        news = context["list_news"].get("items", [])
        smoke.tool("get_news", {"id": news[0]["id"] if news else 9007199254740991}, expect_error=not news)
        found = False
        pipelines = context["list_pipelines"].get("items", [])
        # Ignore prior smoke drafts when locating a real stored event, then
        # inspect a bounded set of existing pipelines without mutating them.
        candidates = [item for item in pipelines if not item.get("name", "").startswith("MCP Smoke ")]
        for item in candidates[:30]:
            events = smoke.tool("list_events", {"pipeline_id": item["id"], "limit": 1}).get("events", [])
            if events:
                smoke.tool("get_event", {"id": events[0]["id"]})
                found = True
                break
        if not found:
            smoke.tool("list_events", {"pipeline_id": 9007199254740991, "limit": 1})
            smoke.tool("get_event", {"id": 9007199254740991}, expect_error=True)
            smoke.record("event_fixture_unavailable", True, detail="get_event_not_found_contract_verified")

    def strategy():
        code = context.get("code", args.code)
        definition = {"name": prefix, "description": "Isolated MCP API smoke fixture", "universe": [code], "period": "1d",
            "body": {"mode": "script", "lang": "starlark", "execution": {"fill_at": "next_open"},
                     "code": "def on_bar(ctx):\n    if not ctx.has_position:\n        return buy(shares=100)\n    if ctx.days_held >= 1:\n        return sell(\"all\")\n    return None\n"}}
        validation = smoke.tool("validate_strategy", {"strategy": definition, "behavior_tests": ["flat_buy_100_exit_after_one_day"]})
        require(validation.get("valid") and validation.get("validation_id"), "strategy_validation_failed")
        arguments = {"strategy": definition, "validation_id": validation["validation_id"]}
        created = smoke.tool("create_strategy", arguments, request_id="stable-create-strategy")
        smoke.evidence["created"]["strategy"] = {"id": created["id"], "name": prefix}
        require(smoke.tool("create_strategy", arguments, request_id="stable-create-strategy") == created,
                "mutation_replay_changed_resource")
        smoke.record("write_idempotent_replay", True)
        changed = copy.deepcopy(arguments)
        changed["strategy"]["name"] += " changed"
        conflict = smoke.tool("create_strategy", changed, request_id="stable-create-strategy", expect_error=True)
        require(conflict == "agent_tool_call_conflict", "request_identity_conflict_not_detected")
        smoke.record("write_idempotency_conflict", True)
        saved = smoke.tool("get_strategy", {"id": created["id"]})
        require(saved.get("name") == prefix, "created_strategy_readback_mismatch")
        definition["description"] += " (updated)"
        validation = smoke.tool("validate_strategy", {"strategy": definition, "behavior_tests": ["flat_buy_100_exit_after_one_day"]})
        require(validation.get("valid"), "updated_strategy_invalid")
        update = {"id": created["id"], "expected_version": saved["version"], "strategy": definition,
                  "validation_id": validation["validation_id"]}
        updated = smoke.tool("update_strategy", update)
        require(updated["version"] > saved["version"], "strategy_version_not_incremented")
        require(smoke.tool("update_strategy", update, expect_error=True) == "resource_version_conflict", "stale_strategy_version_not_rejected")
        context["strategy"] = updated
        smoke.evidence["created"]["strategy"]["version"] = updated["version"]

    def backtest():
        require("strategy" in context and "file" in context, "backtest_dependencies_missing")
        descriptor, saved = context["file"], context["strategy"]
        start, end = descriptor["first_date"][:10], descriptor["last_date"][:10]
        arguments = {"strategy_id": saved["id"], "expected_version": saved["version"], "initial_capital": 1000000,
                     "start_date": start, "end_date": end}
        costs = context.get("list_cost_models", {}).get("items", [])
        if costs:
            arguments["cost_id"] = costs[0]["id"]
        job = smoke.tool("run_backtest", arguments, request_id="stable-backtest")
        smoke.evidence["created"]["backtest"] = {"job_id": job["job_id"], "start_date": start, "end_date": end}
        require(smoke.tool("run_backtest", arguments, request_id="stable-backtest") == job, "backtest_replay_submitted_duplicate")
        deadline = time.monotonic() + args.backtest_wait
        while True:
            current = smoke.tool("get_backtest_job", {"id": job["job_id"]})
            if current["status"] not in ("pending", "running"):
                break
            require(time.monotonic() < deadline, "backtest_completion_timeout")
            time.sleep(1)
        require(current["status"] == "succeed", "backtest_did_not_succeed")
        report = smoke.tool("get_backtest_report", {"id": job["job_id"]})
        require(report.get("job_id") == job["job_id"], "backtest_report_identity_mismatch")
        smoke.evidence["created"]["backtest"]["status"] = current["status"]

    def pipeline():
        types = context.get("node_types", [])
        require(any(item.get("type") == "keyword_filter" for item in types), "safe_keyword_node_unavailable")
        definition = {"name": prefix, "description": "Isolated MCP smoke; no subscriptions, AI or delivery", "sources": [],
                      "nodes": [{"node_key": "safe-filter", "type": "keyword_filter", "name": "Smoke only",
                                 "config": {"keywords": [unique], "list_type": "whitelist"}}], "edges": []}
        require(smoke.tool("validate_pipeline", {"pipeline": definition}).get("valid"), "pipeline_validation_failed")
        preview = smoke.tool("dry_run_pipeline_safe", {"pipeline": definition, "sample_event": {"text": unique}})
        require(preview.get("Status", preview.get("status")) == "passed", "safe_preview_failed")
        require(not preview.get("AIResults", preview.get("ai_results")), "safe_preview_called_model")
        created = smoke.tool("create_pipeline", {"pipeline": definition}, request_id="stable-create-pipeline")
        smoke.evidence["created"]["pipeline"] = {"id": created["id"], "name": prefix, "status": created.get("status")}
        require(smoke.tool("create_pipeline", {"pipeline": definition}, request_id="stable-create-pipeline") == created,
                "pipeline_replay_changed_resource")
        saved = smoke.tool("get_pipeline", {"id": created["id"]})
        require(saved.get("name") == prefix and saved.get("status") == "draft", "pipeline_create_not_draft")
        definition["description"] += " (updated)"
        updated = smoke.tool("update_pipeline", {"id": created["id"], "expected_version": saved["version"], "pipeline": definition})
        version = updated["version"]
        try:
            enabled = smoke.tool("set_pipeline_status", {"id": created["id"], "expected_version": version, "status": "enabled"})
            version = enabled["version"]
            require(enabled.get("status") == "enabled", "pipeline_enable_failed")
        finally:
            # Only the newly created, subscription-free filter is ever targeted.
            current = smoke.tool("get_pipeline", {"id": created["id"]})
            require(current.get("name") == prefix, "pipeline_cleanup_identity_mismatch")
            disabled = smoke.tool("set_pipeline_status", {"id": created["id"], "expected_version": current["version"], "status": "disabled"})
            smoke.evidence["created"]["pipeline"].update({"status": disabled.get("status"), "version": disabled["version"]})
            require(disabled.get("status") == "disabled", "pipeline_disable_failed")
        smoke.tool("list_events", {"pipeline_id": created["id"], "limit": 1})
        smoke.record("safe_pipeline_roundtrip", True)

    for label, function in [("market", market), ("queries", queries), ("strategy", strategy), ("backtest", backtest), ("pipeline", pipeline)]:
        smoke.phase(label, function)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True)
    parser.add_argument("--token-file", required=True)
    parser.add_argument("--output", help="Also write redacted JSON evidence to this path")
    parser.add_argument("--code", default="sh.600519", help="An existing local instrument; default sh.600519")
    parser.add_argument("--backtest-wait", type=int, default=90)
    args = parser.parse_args()
    parsed = urllib.parse.urlsplit(args.url)
    require(parsed.scheme in ("http", "https") and parsed.hostname and not parsed.username and not parsed.password
            and not parsed.query and not parsed.fragment, "invalid_endpoint")
    token = pathlib.Path(args.token_file).read_text().strip()
    require(bool(token), "empty_token_file")
    smoke = Smoke(args.url, token)
    try:
        smoke.phase("protocol_and_catalog", lambda: run(smoke, args))
    finally:
        if smoke.session:
            try:
                status, _, _ = smoke.http(method="DELETE")
                smoke.record("primary_session_closed", status == 200)
                status, _, _ = smoke.http(body={"jsonrpc": "2.0", "id": "after-close", "method": "ping"})
                smoke.record("closed_session_rejected", status == 404)
            except (urllib.error.URLError, OSError, CheckFailure) as error:
                smoke.record("session_cleanup", False, error=type(error).__name__)
    tested = set(smoke.evidence["tools"])
    succeeded = {name for name, stats in smoke.evidence["tools"].items() if stats["successes"] > 0}
    smoke.evidence["tested_tool_count"] = len(tested)
    smoke.evidence["successful_tool_count"] = len(succeeded)
    smoke.evidence["untested_tools"] = sorted(EXPECTED - tested)
    smoke.evidence["tools_without_successful_call"] = sorted(EXPECTED - succeeded)
    smoke.record("all_31_tools_exercised", tested == EXPECTED)
    smoke.record("all_31_tools_successful", succeeded == EXPECTED)
    smoke.evidence["ok"] = all(check["ok"] for check in smoke.evidence["checks"])
    smoke.evidence["finished_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
    serialized = json.dumps(smoke.evidence, ensure_ascii=False, indent=2) + "\n"
    if args.output:
        pathlib.Path(args.output).write_text(serialized)
    sys.stdout.write(serialized)
    return 0 if smoke.evidence["ok"] else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (CheckFailure, OSError) as error:
        sys.stderr.write(json.dumps({"ok": False, "error": str(error) if isinstance(error, CheckFailure) else type(error).__name__}) + "\n")
        sys.exit(1)
