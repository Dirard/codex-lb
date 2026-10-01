"""Compare local Go/legacy runtimes on identical offline JSON/SSE/cancellation loads.

Uses only temporary databases and a loopback Chat Completions/Responses stub. Never loads
the installation environment or account data. This is a synthetic-workload check,
not a prediction of production streaming throughput. Cancellation is confirmed
by the stub observing the upstream socket close after the client stops reading.
"""

import argparse
import concurrent.futures
import http.client
import http.server
import json
import os
import socket
import statistics
import subprocess
import tempfile
import threading
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ANSWER = json.dumps({
    "id": "bench-response", "object": "chat.completion", "model": "bench-model",
    "choices": [{"index": 0, "message": {"role": "assistant", "content": "offline answer"}, "finish_reason": "stop"}],
    "usage": {"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": 20},
}).encode()
STREAM_CONDITION = threading.Condition()
STREAM_ENDS = {}
STREAM_TIMINGS = {}
STREAM_CHUNKS = 5
PROMPT_BYTES = 0
STREAM_ACTIVE = 0
STREAM_PEAK = 0
CHUNK_TEXT = "offline "
REQUIRED_ACTIVE = 0
UPSTREAM_PROTOCOL = "chat"
RESPONSE_INPUT_ARRAY = False
RESPONSE = {"id": "bench-response", "object": "response", "model": "bench-model", "status": "completed",
            "output": [{"id": "bench-message", "type": "message", "role": "assistant", "status": "completed",
                        "content": [{"type": "output_text", "text": "offline answer", "annotations": []}]}],
            "usage": {"input_tokens": 12, "output_tokens": 8, "total_tokens": 20}}


class StubServer(http.server.ThreadingHTTPServer):
    # The stub must accept the offered load rather than manufacture SYN retries.
    request_queue_size = 512


class Stub(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def setup(self):
        super().setup()
        self.connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)

    def log_message(self, *_args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
        if body.get("stream"):
            return self.stream(body)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        answer = json.dumps(RESPONSE).encode() if UPSTREAM_PROTOCOL == "responses" else ANSWER
        self.send_header("Content-Length", str(len(answer)))
        self.end_headers()
        self.wfile.write(answer)

    def stream(self, body):
        global STREAM_ACTIVE, STREAM_PEAK
        identity = body["input"] if UPSTREAM_PROTOCOL == "responses" else body["messages"][0]["content"]
        if UPSTREAM_PROTOCOL == "responses" and isinstance(identity, list):
            identity = identity[0]["content"]
        if isinstance(identity, list):
            identity = identity[0]["text"]
        identity = identity.split("\n", 1)[0]
        arrived = time.perf_counter()
        with STREAM_CONDITION:
            STREAM_ACTIVE += 1
            STREAM_PEAK = max(STREAM_PEAK, STREAM_ACTIVE)
            STREAM_CONDITION.notify_all()
        cancelled = False
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True
        try:
            # A bounded paced stream makes the cancellation observation explicit.
            count = 200 if identity.startswith("cancel:") else 5 if identity.startswith("warmup:") else STREAM_CHUNKS
            if UPSTREAM_PROTOCOL == "responses":
                self.wfile.write(b'data: {"type":"response.created","response":{"id":"bench-response","status":"in_progress"}}\n\n')
            for index in range(count):
                chunk = {"id": "bench-stream", "choices": [{"index": 0,
                         "delta": {"content": CHUNK_TEXT}, "finish_reason": None}]}
                if UPSTREAM_PROTOCOL == "responses":
                    chunk = {"type": "response.output_text.delta", "delta": CHUNK_TEXT}
                if index == 0:
                    with STREAM_CONDITION:
                        STREAM_TIMINGS[identity] = (arrived, time.perf_counter())
                self.wfile.write(b"data: " + json.dumps(chunk).encode() + b"\n\n")
                self.wfile.flush()
                if index == 0 and identity.startswith("stream:") and REQUIRED_ACTIVE:
                    with STREAM_CONDITION:
                        if not STREAM_CONDITION.wait_for(lambda: STREAM_PEAK >= REQUIRED_ACTIVE, timeout=30):
                            self.wfile.write(b'data: {"type":"error","error":{"code":"benchmark_overlap","message":"Required active stream count was not reached"}}\n\n')
                            self.wfile.flush()
                            return
                time.sleep(0.02)
            terminal = {"id": "bench-stream", "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                        "usage": {"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": 20}}
            if UPSTREAM_PROTOCOL == "responses":
                terminal = {"type": "response.completed", "response": RESPONSE}
            self.wfile.write(b"data: " + json.dumps(terminal).encode() + b"\n\ndata: [DONE]\n\n")
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            cancelled = True
        finally:
            with STREAM_CONDITION:
                STREAM_ACTIVE -= 1
                if identity.startswith("cancel:"):
                    STREAM_ENDS[identity] = (time.perf_counter(), cancelled)
                STREAM_CONDITION.notify_all()


def request(port, route, method="GET", body=None, headers=None):
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
    try:
        data = json.dumps(body) if body is not None else None
        conn.request(method, route, data, {"Content-Type": "application/json", **(headers or {})})
        result = conn.getresponse()
        raw = result.read()
        if result.status not in (200, 201):
            raise RuntimeError(f"{method} {route}: HTTP {result.status}")
        return json.loads(raw), result.getheader("Set-Cookie", "").split(";", 1)[0]
    finally:
        conn.close()


def counters(pid):
    # Linux process counters avoid including the client/stub in server CPU/RSS.
    stat = Path(f"/proc/{pid}/stat").read_text(encoding="utf-8").rsplit(")", 1)[1].split()
    cpu = (int(stat[11]) + int(stat[12])) / os.sysconf("SC_CLK_TCK")
    status = Path(f"/proc/{pid}/status").read_text(encoding="utf-8")
    rss = next(int(line.split()[1]) for line in status.splitlines() if line.startswith("VmRSS:"))
    hwm = next(int(line.split()[1]) for line in status.splitlines() if line.startswith("VmHWM:"))
    return cpu, rss / 1024, len(list(Path(f"/proc/{pid}/fd").iterdir())), len(list(Path(f"/proc/{pid}/task").iterdir())), hwm / 1024


def streaming_request(port, key, workload, protocol="chat"):
    identity = f"{workload}:{time.perf_counter_ns()}"
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
    response = None
    started = time.perf_counter()
    first = None
    usage = None
    content_chars = 0
    expected_chars = len(CHUNK_TEXT) * (5 if workload == "warmup" else STREAM_CHUNKS)
    route = "/v1/chat/completions" if protocol == "chat" else "/backend-api/codex/responses"
    payload = {"model": "bench-model", "stream": True}
    prompt = identity + ("\n" + "x" * PROMPT_BYTES if PROMPT_BYTES else "")
    if protocol == "chat":
        payload.update(messages=[{"role": "user", "content": prompt}], stream_options={"include_usage": True})
    else:
        payload["input"] = [{"role": "user", "content": [{"type": "input_text", "text": prompt}]}] if RESPONSE_INPUT_ARRAY else prompt

    def metrics(ended):
        with STREAM_CONDITION:
            arrived, sent = STREAM_TIMINGS.pop(identity)
        return {"latency": (ended-started)*1000, "ttft": (first-started)*1000,
                "before_upstream": (arrived-started)*1000, "forward_first": (first-sent)*1000}

    try:
        conn.request("POST", route, json.dumps(payload), {"Content-Type": "application/json", "Authorization": "Bearer " + key})
        response = conn.getresponse()
        if response.status != 200:
            raise RuntimeError(f"benchmark stream: HTTP {response.status}")
        while line := response.readline():
            if not line.startswith(b"data:"):
                continue
            data = line[5:].strip()
            if data == b"[DONE]":
                if usage != 20 or first is None or content_chars != expected_chars:
                    raise RuntimeError("benchmark stream lost content or usage")
                return metrics(time.perf_counter())
            event = json.loads(data)
            if event.get("error") or event.get("type") in ("response.failed", "error"):
                raise RuntimeError("benchmark stream returned an error")
            if event.get("type") == "response.completed":
                usage = event.get("response", {}).get("usage", {}).get("total_tokens")
                if usage != 20 or first is None or content_chars != expected_chars:
                    raise RuntimeError("benchmark Responses lost content or usage")
                return metrics(time.perf_counter())
            if event.get("usage"):
                usage = event["usage"].get("total_tokens")
            content = event.get("delta", "") if event.get("type") == "response.output_text.delta" else "".join(
                choice.get("delta", {}).get("content", "") or "" for choice in event.get("choices", []))
            if content != CHUNK_TEXT * (len(content) // len(CHUNK_TEXT)):
                raise RuntimeError("benchmark stream changed content")
            content_chars += len(content)
            if first is None and content:
                first = time.perf_counter()
                if workload == "cancel":
                    closed = time.perf_counter()
                    response.close()
                    conn.close()
                    with STREAM_CONDITION:
                        if not STREAM_CONDITION.wait_for(lambda: identity in STREAM_ENDS, timeout=6):
                            raise RuntimeError("provider did not observe cancellation")
                        ended, cancelled = STREAM_ENDS.pop(identity)
                    if not cancelled:
                        raise RuntimeError("provider ran to completion after client cancellation")
                    return {**metrics(first), "cancel_release": max(0, ended-closed)*1000}
        raise RuntimeError("benchmark stream ended without terminal marker")
    finally:
        if response is not None:
            response.close()
        conn.close()
        with STREAM_CONDITION:
            STREAM_TIMINGS.pop(identity, None)


def trial(runtime, upstream_port, count, concurrency, workload, *, binary=None, key_count=1, protocol="chat", metered=False, go_procs=0, go_memory_limit=None, single_cpu=False):
    global STREAM_PEAK
    with tempfile.TemporaryDirectory(prefix="codex-lb-benchmark-") as directory:
        with socket.socket() as socket_:
            socket_.bind(("127.0.0.1", 0))
            port = socket_.getsockname()[1]
        environment = {"PATH": os.environ.get("PATH", "/usr/bin:/bin")}
        if runtime != "legacy":
            if go_procs:
                environment["GOMAXPROCS"] = str(go_procs)
            if go_memory_limit:
                environment["GOMEMLIMIT"] = go_memory_limit
            command = [str(binary or ROOT / "bin/codex-lb"), "serve", "--listen", f"127.0.0.1:{port}", "--data-dir", directory, "--shutdown-grace", "0s"]
        else:
            environment.update({
                "PYTHONPATH": str(ROOT / "legacy"), "PYTHONDONTWRITEBYTECODE": "1",
                "CODEX_LB_ENV_FILE": "/dev/null", "CODEX_LB_DATA_DIR": directory,
                "CODEX_LB_TELEMETRY_ENABLED": "false",
                "CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN": "synthetic-benchmark-token",
                "CODEX_LB_USAGE_REFRESH_ENABLED": "false",
                "CODEX_LB_RATE_LIMIT_RESET_CREDITS_REFRESH_ENABLED": "false",
                "CODEX_LB_AUTH_GUARDIAN_ENABLED": "false",
                "CODEX_LB_QUOTA_PLANNER_SCHEDULER_ENABLED": "false",
                "CODEX_LB_AUTOMATIONS_SCHEDULER_ENABLED": "false",
            })
            # Refuse any non-loopback outbound socket from the legacy process.
            launcher = """
import ipaddress, sys
def offline(event, args):
    if event == 'socket.connect' and isinstance(args[1], tuple):
        if not ipaddress.ip_address(args[1][0]).is_loopback:
            raise PermissionError('benchmark permits only loopback traffic')
sys.addaudithook(offline)
import uvicorn
uvicorn.run('app.main:app', host='127.0.0.1', port=int(sys.argv[1]), log_level='critical', access_log=False)
"""
            command = [str(ROOT / "legacy/.venv/bin/python"), "-c", launcher, str(port)]
        cpu_id = min(os.sched_getaffinity(0)) if single_cpu else None
        if cpu_id is not None:
            command = ["taskset", "--cpu-list", str(cpu_id), *command]
        with tempfile.TemporaryFile() as log:
            started = time.perf_counter()
            process = subprocess.Popen(command, cwd=directory, env=environment, stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 45
                while True:
                    if process.poll() is not None:
                        raise RuntimeError(f"{runtime} exited before readiness ({process.returncode})")
                    try:
                        request(port, "/health/ready")
                        break
                    except (OSError, RuntimeError):
                        if time.monotonic() >= deadline:
                            raise RuntimeError(f"{runtime} readiness timeout")
                        time.sleep(0.025)
                startup_ms = (time.perf_counter() - started) * 1000
                _, cookie = request(port, "/api/dashboard-auth/password/setup", "POST", {"password": "synthetic-benchmark-password", "bootstrapToken": "synthetic-benchmark-token"})
                admin = {"Cookie": cookie}
                source, _ = request(port, "/api/model-sources/", "POST", {
                    "name": "Offline benchmark", "baseUrl": f"http://127.0.0.1:{upstream_port}/v1",
                    "supportsResponses": UPSTREAM_PROTOCOL == "responses",
                    "models": [{"model": "bench-model", "inputPer1M": 1, "outputPer1M": 2}],
                }, admin)
                limits = [{"limitType": "total_tokens", "limitWindow": "weekly", "maxValue": 1_000_000_000}] if metered else []
                keys = [request(port, "/api/api-keys/", "POST", {"name": f"Offline benchmark {i}", "assignedSourceIds": [source["id"]], "limits": limits}, admin)[0]
                        for i in range(key_count)]

                def respond(_index, warmup=False):
                    key = keys[_index % len(keys)]
                    if workload != "json":
                        phase = "warmup" if warmup and workload == "stream" else workload
                        return streaming_request(port, key["key"], phase, protocol)
                    before = time.perf_counter()
                    route = "/v1/chat/completions" if protocol == "chat" else "/backend-api/codex/responses"
                    payload = {"model": "bench-model", "stream": False}
                    prompt = "offline prompt" + "x" * PROMPT_BYTES
                    response_input = [{"role": "user", "content": [{"type": "input_text", "text": prompt}]}] if RESPONSE_INPUT_ARRAY else prompt
                    payload.update({"messages": [{"role": "user", "content": prompt}]} if protocol == "chat" else {"input": response_input})
                    result, _ = request(port, route, "POST", payload, {"Authorization": "Bearer " + key["key"]})
                    if result.get("usage", {}).get("total_tokens") != 20:
                        raise RuntimeError("benchmark response lost usage")
                    return {"latency": (time.perf_counter() - before) * 1000}

                for i in range(8):
                    respond(i, warmup=True)
                with STREAM_CONDITION:
                    if not STREAM_CONDITION.wait_for(lambda: STREAM_ACTIVE == 0, timeout=10):
                        raise RuntimeError("warmup streams were not released")
                    STREAM_PEAK = 0
                cpu_before, rss_before, fd_before, threads_before, hwm_before = counters(process.pid)
                peak = {"rss_mib": hwm_before, "fds": fd_before, "threads": threads_before}
                steady = []
                stop_sampling = threading.Event()

                def sample():
                    while not stop_sampling.wait(0.02):
                        cpu, _, fds, threads, hwm = counters(process.pid)
                        for name, value in (("rss_mib", hwm), ("fds", fds), ("threads", threads)):
                            peak[name] = max(peak[name], value)
                        with STREAM_CONDITION:
                            full = REQUIRED_ACTIVE and STREAM_ACTIVE == REQUIRED_ACTIVE
                        if full:
                            point = (time.perf_counter(), cpu)
                            if not steady:
                                steady.extend([point, point])
                            steady[1] = point

                sampler = threading.Thread(target=sample, daemon=True)
                sampler.start()
                before = time.perf_counter()
                try:
                    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
                        results = list(pool.map(respond, range(count)))
                finally:
                    stop_sampling.set()
                    sampler.join()
                elapsed = time.perf_counter() - before
                with STREAM_CONDITION:
                    if not STREAM_CONDITION.wait_for(lambda: STREAM_ACTIVE == 0, timeout=10):
                        raise RuntimeError("upstream streams leaked after workload")
                    if workload == "stream" and STREAM_PEAK < REQUIRED_ACTIVE:
                        raise RuntimeError(f"only {STREAM_PEAK} of {REQUIRED_ACTIVE} streams active")
                cpu_after, rss_after, fd_after, threads_after, hwm_after = counters(process.pid)
                output = {"runtime": runtime, "workload": workload, "protocol": protocol, "requests": count, "concurrency": concurrency, "keys": key_count, "metered": metered, "errors": 0,
                        "startup_ms": round(startup_ms, 2), "cpu_ms": round((cpu_after-cpu_before)*1000, 2),
                        "rss_before_mib": round(rss_before, 2), "rss_after_mib": round(rss_after, 2),
                        "fd_before": fd_before, "fd_after": fd_after,
                        "threads_before": threads_before, "threads_after": threads_after,
                        "requests_per_second": round(count/elapsed, 2),
                        "server_cpu_percent": round((cpu_after-cpu_before)/elapsed*100, 2),
                        "gomaxprocs": go_procs or None, "gomemlimit": go_memory_limit, "prompt_bytes": PROMPT_BYTES, "stream_chunks": STREAM_CHUNKS,
                        "peak_rss_mib": round(max(peak["rss_mib"], hwm_after), 2), "peak_fds": peak["fds"], "peak_threads": peak["threads"],
                        "peak_upstream_streams": STREAM_PEAK, "remaining_upstream_streams": STREAM_ACTIVE,
                        "upstream_protocol": UPSTREAM_PROTOCOL, "required_active_streams": REQUIRED_ACTIVE,
                        "server_cpu_id": cpu_id,
                        "response_input_shape": ("messages" if RESPONSE_INPUT_ARRAY else "text") if protocol == "responses" else None,
                        "stream_content_verified": workload == "stream",
                        }
                if steady and steady[1][0] - steady[0][0] >= 1:
                    duration = steady[1][0] - steady[0][0]
                    output["all_streams_active_seconds"] = round(duration, 2)
                    output["steady_server_cpu_percent"] = round((steady[1][1]-steady[0][1])/duration*100, 2)
                for metric in results[0]:
                    values = sorted(result[metric] for result in results)
                    prefix = "" if metric == "latency" else metric + "_"
                    output[prefix + "p50_ms"] = round(statistics.median(values), 2)
                    output[prefix + "p95_ms"] = round(values[(len(values)*95+99)//100-1], 2)
                    output[prefix + "p99_ms"] = round(values[(len(values)*99+99)//100-1], 2)
                if metered and workload != "cancel":
                    actual, _ = request(port, "/api/api-keys/", headers=admin)
                    by_id = {key["id"]: key for key in actual}
                    for index, key in enumerate(keys):
                        expected = 20 * (len(range(index, count, key_count)) + len(range(index, 8, key_count)))
                        if by_id[key["id"]]["limits"][0]["currentValue"] != expected:
                            raise RuntimeError("benchmark key accounting mismatch")
                    output["key_accounting_verified"] = True
                return output
            finally:
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()


def main():
    global STREAM_CHUNKS, PROMPT_BYTES, REQUIRED_ACTIVE, UPSTREAM_PROTOCOL, RESPONSE_INPUT_ARRAY
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runs", type=int, default=3)
    parser.add_argument("--requests", type=int, default=128)
    parser.add_argument("--concurrency", type=int, default=8)
    parser.add_argument("--workload", choices=("json", "stream", "cancel"), default="json")
    parser.add_argument("--protocol", choices=("chat", "responses"), default="chat")
    parser.add_argument("--upstream-protocol", choices=("chat", "responses"), default="chat")
    parser.add_argument("--input-array", action="store_true", help="use Codex-style Responses input messages instead of scalar text")
    parser.add_argument("--require-active", type=int, default=0, help="hold streams after first event until this many are active")
    parser.add_argument("--keys", type=int, default=1)
    parser.add_argument("--metered", action="store_true", help="exercise and verify independent per-key token limits")
    parser.add_argument("--stream-chunks", type=int, default=5, help="20ms per chunk; use larger counts for sustained overlap")
    parser.add_argument("--go-binary", type=Path, default=ROOT / "bin/codex-lb")
    parser.add_argument("--go-procs", type=int, default=0, help="GOMAXPROCS for Go processes; 0 uses runtime default")
    parser.add_argument("--single-cpu", action="store_true", help="pin only the server process to one available Linux CPU")
    parser.add_argument("--go-memory-limit", help="optional standard Go GOMEMLIMIT, e.g. 96MiB")
    parser.add_argument("--prompt-bytes", type=int, default=0, help="append this many ASCII bytes to each request prompt")
    parser.add_argument("--baseline-binary", type=Path)
    parser.add_argument("--runtimes", nargs="+", choices=("legacy", "go", "baseline"), default=["legacy", "go"])
    args = parser.parse_args()
    if min(args.runs, args.requests, args.concurrency, args.keys, args.stream_chunks) < 1 or args.runs > 10 or args.requests > 10000 or args.concurrency > 256 or args.keys > 256 or args.stream_chunks > 1000:
        parser.error("expected runs 1..10, requests 1..10000, concurrency 1..256, keys 1..256, stream-chunks 1..1000")
    if "baseline" in args.runtimes and args.baseline_binary is None:
        parser.error("baseline runtime requires --baseline-binary")
    if not 0 <= args.require_active <= min(args.requests, args.concurrency) or args.require_active and args.workload != "stream":
        parser.error("require-active needs stream workload and must fit requests/concurrency")
    if args.require_active and (args.requests != args.concurrency or args.require_active != args.concurrency):
        parser.error("require-active measures one full concurrent wave: requests, concurrency and require-active must match")
    if args.upstream_protocol == "responses" and args.protocol != "responses":
        parser.error("Responses upstream requires Responses client protocol")
    if not 0 <= args.go_procs <= 128 or not 0 <= args.prompt_bytes <= 2*1024*1024:
        parser.error("expected go-procs 0..128 and prompt-bytes 0..2097152")
    STREAM_CHUNKS = args.stream_chunks
    PROMPT_BYTES = args.prompt_bytes
    REQUIRED_ACTIVE, UPSTREAM_PROTOCOL = args.require_active, args.upstream_protocol
    RESPONSE_INPUT_ARRAY = args.input_array
    upstream = StubServer(("127.0.0.1", 0), Stub)
    worker = threading.Thread(target=upstream.serve_forever, daemon=True)
    worker.start()
    try:
        for run in range(args.runs):
            # Alternate order to avoid always giving one runtime a warmer host.
            for runtime in (args.runtimes if run % 2 == 0 else list(reversed(args.runtimes))):
                result = trial(runtime, upstream.server_port, args.requests, args.concurrency, args.workload,
                               binary=(args.baseline_binary if runtime == "baseline" else args.go_binary).resolve(),
                               key_count=args.keys, protocol=args.protocol, metered=args.metered,
                               go_procs=args.go_procs, go_memory_limit=args.go_memory_limit, single_cpu=args.single_cpu)
                print(json.dumps({"run": run+1, **result}), flush=True)
    finally:
        upstream.shutdown()
        upstream.server_close()
        worker.join()


if __name__ == "__main__":
    main()
