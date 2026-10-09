"""Compare local Go/legacy runtimes on identical offline JSON/SSE/cancellation loads.

Uses only temporary databases and a loopback Chat Completions/Responses stub. Never loads
the installation environment or account data. This is a synthetic-workload check,
not a prediction of production streaming throughput. Cancellation is confirmed
by the stub observing the upstream socket close after the client stops reading.
"""

import argparse
import base64
import concurrent.futures
import hashlib
import http.client
import http.server
import json
import os
import random
import select
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
STREAM_ABORTED = False
UPSTREAM_WS_ACTIVE = 0
CHUNK_TEXT = "offline "
REQUIRED_ACTIVE = 0
RAMP_SECONDS = 0.0
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

    def do_GET(self):
        global UPSTREAM_WS_ACTIVE
        key = self.headers.get("Sec-WebSocket-Key", "")
        if self.path != "/v1/responses" or self.headers.get("Upgrade", "").lower() != "websocket" or not key:
            self.send_error(404)
            return
        accepted = base64.b64encode(hashlib.sha1(key.encode() + b"258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest()).decode()
        self.send_response(101, "Switching Protocols")
        self.send_header("Connection", "Upgrade")
        self.send_header("Upgrade", "websocket")
        self.send_header("Sec-WebSocket-Accept", accepted)
        self.end_headers()
        self.close_connection = True
        self.websocket_poll = select.poll()
        self.websocket_poll.register(self.connection, select.POLLIN)
        with STREAM_CONDITION:
            UPSTREAM_WS_ACTIVE += 1
        try:
            while True:
                try:
                    frame = read_websocket_message(self.connection, from_client=True)
                except (EOFError, OSError):
                    return
                if frame is None:
                    return
                body = json.loads(frame)
                if body.get("type") != "response.create":
                    raise RuntimeError("upstream WebSocket expected response.create")
                self.stream(body, websocket=True)
        finally:
            with STREAM_CONDITION:
                UPSTREAM_WS_ACTIVE -= 1
                STREAM_CONDITION.notify_all()

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

    def pump_websocket_control(self, timeout):
        if self.websocket_poll.poll(max(1, int(timeout * 1000))):
            read_websocket_message(self.connection, from_client=True, control_only=True)

    def stream(self, body, websocket=False):
        global STREAM_ACTIVE, STREAM_PEAK
        identity = body["input"] if UPSTREAM_PROTOCOL == "responses" else body["messages"][0]["content"]
        if UPSTREAM_PROTOCOL == "responses" and isinstance(identity, list):
            identity = identity[0]["content"]
        if isinstance(identity, list):
            identity = identity[0]["text"]
        identity = identity.split("\n", 1)[0]
        response_id = "bench-response-" + identity.replace(":", "-")
        arrived = time.perf_counter()
        with STREAM_CONDITION:
            STREAM_ACTIVE += 1
            STREAM_PEAK = max(STREAM_PEAK, STREAM_ACTIVE)
            STREAM_CONDITION.notify_all()
        cancelled = False
        if not websocket:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Connection", "close")
            self.end_headers()
            self.close_connection = True

        def write_event(event):
            data = json.dumps(event).encode()
            if websocket:
                send_websocket_frame(self.connection, 1, data, masked=False)
            else:
                self.wfile.write(b"data: " + data + b"\n\n")
                self.wfile.flush()

        try:
            # A bounded paced stream makes the cancellation observation explicit.
            count = 200 if identity.startswith("cancel:") else 5 if identity.startswith("warmup:") else STREAM_CHUNKS
            if UPSTREAM_PROTOCOL == "responses":
                write_event({"type": "response.created", "response": {"id": response_id, "status": "in_progress"}})
            for index in range(count):
                chunk = {"id": "bench-stream", "choices": [{"index": 0,
                         "delta": {"content": CHUNK_TEXT}, "finish_reason": None}]}
                if UPSTREAM_PROTOCOL == "responses":
                    chunk = {"type": "response.output_text.delta", "delta": CHUNK_TEXT}
                if index == 0:
                    with STREAM_CONDITION:
                        STREAM_TIMINGS[identity] = (arrived, time.perf_counter())
                write_event(chunk)
                if index == 0 and identity.startswith("stream:") and REQUIRED_ACTIVE and not RAMP_SECONDS:
                    if websocket:
                        deadline = time.monotonic() + 90
                        while True:
                            with STREAM_CONDITION:
                                if STREAM_ABORTED:
                                    return
                                if STREAM_PEAK >= REQUIRED_ACTIVE:
                                    break
                            remaining = deadline - time.monotonic()
                            if remaining <= 0:
                                write_event({"type": "error", "error": {"code": "benchmark_overlap", "message": "Required active stream count was not reached"}})
                                return
                            self.pump_websocket_control(min(0.25, remaining))
                    else:
                        with STREAM_CONDITION:
                            reached = STREAM_CONDITION.wait_for(lambda: STREAM_PEAK >= REQUIRED_ACTIVE or STREAM_ABORTED, timeout=90)
                            aborted = STREAM_ABORTED
                        if aborted:
                            return
                        if not reached:
                            write_event({"type": "error", "error": {"code": "benchmark_overlap", "message": "Required active stream count was not reached"}})
                            return
                if websocket:
                    self.pump_websocket_control(0.02)
                else:
                    time.sleep(0.02)
            terminal = {"id": "bench-stream", "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                        "usage": {"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": 20}}
            if UPSTREAM_PROTOCOL == "responses":
                terminal = {"type": "response.completed", "response": {**RESPONSE, "id": response_id}}
            write_event(terminal)
            if not websocket:
                self.wfile.write(b"data: [DONE]\n\n")
                self.wfile.flush()
        except (EOFError, OSError):
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


def receive_exact(sock, count):
    data = bytearray()
    while len(data) < count:
        chunk = sock.recv(count - len(data))
        if not chunk:
            raise EOFError("WebSocket closed while reading a frame")
        data.extend(chunk)
    return bytes(data)


def masked_websocket_payload(payload, mask):
    result = bytearray(payload)
    for offset, key in enumerate(mask):
        result[offset::4] = payload[offset::4].translate(bytes(value ^ key for value in range(256)))
    return bytes(result)


def send_websocket_frame(sock, opcode, payload=b"", *, masked=True):
    size = len(payload)
    mask_bit = 0x80 if masked else 0
    if size < 126:
        length = bytes((mask_bit | size,))
    elif size < 65536:
        length = bytes((mask_bit | 126,)) + size.to_bytes(2, "big")
    else:
        length = bytes((mask_bit | 127,)) + size.to_bytes(8, "big")
    if masked:
        mask = os.urandom(4)
        payload = mask + masked_websocket_payload(payload, mask)
    sock.sendall(bytes((0x80 | opcode,)) + length + payload)


def read_websocket_message(sock, *, from_client=False, control_only=False):
    fragments = []
    total = 0
    while True:
        first, second = receive_exact(sock, 2)
        size = second & 0x7f
        if bool(second & 0x80) != from_client:
            raise RuntimeError("WebSocket frame has the wrong mask bit")
        if size == 126:
            size = int.from_bytes(receive_exact(sock, 2), "big")
        elif size == 127:
            size = int.from_bytes(receive_exact(sock, 8), "big")
        if size > 32 << 20:
            raise RuntimeError("benchmark WebSocket frame is too large")
        mask = receive_exact(sock, 4) if from_client else None
        payload = receive_exact(sock, size)
        if mask is not None:
            payload = masked_websocket_payload(payload, mask)
        opcode = first & 0x0f
        if opcode >= 8 and (not first & 0x80 or size > 125):
            raise RuntimeError("invalid WebSocket control frame")
        if opcode == 9:
            send_websocket_frame(sock, 10, payload, masked=not from_client)
            if control_only:
                return
            continue
        if opcode == 10:
            if control_only:
                return
            continue
        if opcode == 8:
            if from_client:
                send_websocket_frame(sock, 8, payload, masked=False)
                if control_only:
                    raise EOFError("upstream WebSocket closed during response")
                return None
            status = int.from_bytes(payload[:2], "big") if len(payload) >= 2 else None
            raise RuntimeError(f"WebSocket closed before terminal response ({status})")
        if control_only:
            raise RuntimeError(f"unexpected WebSocket data opcode {opcode} during response")
        if opcode == 1 and not fragments:
            fragments.append(payload)
        elif opcode == 0 and fragments:
            fragments.append(payload)
        else:
            raise RuntimeError(f"unexpected WebSocket opcode {opcode}")
        total += size
        if total > 32 << 20:
            raise RuntimeError("benchmark WebSocket message is too large")
        if first & 0x80:
            return b"".join(fragments)


def open_idle_websocket(port, key):
    sock = socket.create_connection(("127.0.0.1", port), timeout=60)
    try:
        sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        encoded_nonce = base64.b64encode(os.urandom(16))
        expected = base64.b64encode(hashlib.sha1(encoded_nonce + b"258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest())
        request = (f"GET /backend-api/codex/responses HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n"
                   "Authorization: Bearer " + key + "\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"
                   "Sec-WebSocket-Key: " + encoded_nonce.decode() + "\r\nSec-WebSocket-Version: 13\r\n\r\n")
        sock.sendall(request.encode())
        response = b""
        while b"\r\n\r\n" not in response:
            chunk = sock.recv(4096)
            if not chunk:
                raise RuntimeError("idle WebSocket upgrade closed")
            response += chunk
            if len(response) > 65536:
                raise RuntimeError("idle WebSocket upgrade response is too large")
        headers = dict(line.split(b":", 1) for line in response.split(b"\r\n\r\n", 1)[0].split(b"\r\n")[1:] if b":" in line)
        headers = {name.strip().lower(): value.strip() for name, value in headers.items()}
        if not response.startswith(b"HTTP/1.1 101 ") or headers.get(b"sec-websocket-accept") != expected:
            raise RuntimeError("idle WebSocket upgrade was not accepted")
        if response.split(b"\r\n\r\n", 1)[1]:
            raise RuntimeError("idle WebSocket sent data before response.create")
        sock.settimeout(5)
        return sock
    except BaseException:
        sock.close()
        raise


def verify_idle_websocket(sock, index):
    payload = f"benchmark-idle-{index}".encode()
    send_websocket_frame(sock, 9, payload)
    head = receive_exact(sock, 2)
    if head[0] & 0x0f != 10 or head[1] != len(payload):
        raise RuntimeError("idle WebSocket did not return its pong")
    if receive_exact(sock, len(payload)) != payload:
        raise RuntimeError("idle WebSocket pong payload changed")


def close_idle_websockets(socks):
    for sock in socks:
        try:
            sock.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        sock.close()


def streaming_request(port, key, workload, protocol="chat", client_websocket=False, prompt_bytes=None):
    identity = f"{workload}:{time.perf_counter_ns()}"
    conn = None
    response = None
    sock = None
    started = time.perf_counter()
    first = None
    usage = None
    content_chars = 0
    expected_chars = len(CHUNK_TEXT) * (5 if workload == "warmup" else STREAM_CHUNKS)
    route = "/v1/chat/completions" if protocol == "chat" else "/backend-api/codex/responses"
    payload = {"model": "bench-model", "stream": True}
    size = PROMPT_BYTES if prompt_bytes is None else prompt_bytes
    prompt = identity + ("\n" + "x" * size if size else "")
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
        if client_websocket:
            sock = open_idle_websocket(port, key)
            sock.settimeout(60)
            send_websocket_frame(sock, 1, json.dumps({"type": "response.create", **payload}).encode())
        else:
            conn = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
            conn.request("POST", route, json.dumps(payload), {"Content-Type": "application/json", "Authorization": "Bearer " + key})
            response = conn.getresponse()
            if response.status != 200:
                raise RuntimeError(f"benchmark stream: HTTP {response.status}")
        while True:
            if sock is not None:
                data = read_websocket_message(sock)
            else:
                line = response.readline()
                if not line:
                    raise RuntimeError("benchmark stream ended without terminal marker")
                if not line.startswith(b"data:"):
                    continue
                data = line[5:].strip()
            if data == b"[DONE]":
                if usage != 20 or first is None or content_chars != expected_chars:
                    raise RuntimeError("benchmark stream lost content or usage")
                return metrics(time.perf_counter())
            event = json.loads(data)
            if event.get("error") or event.get("type") in ("response.failed", "error"):
                detail = event.get("error") or event.get("response", {}).get("error") or {}
                code = str(detail.get("code", event.get("type", "unknown")))[:80]
                raise RuntimeError(f"benchmark stream returned an error: {code}")
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
                    if sock is not None:
                        send_websocket_frame(sock, 1, b'{"type":"response.cancel"}')
                    else:
                        response.close()
                        conn.close()
                    with STREAM_CONDITION:
                        if not STREAM_CONDITION.wait_for(lambda: identity in STREAM_ENDS, timeout=6):
                            raise RuntimeError("provider did not observe cancellation")
                        ended, cancelled = STREAM_ENDS.pop(identity)
                    if not cancelled:
                        raise RuntimeError("provider ran to completion after client cancellation")
                    return {**metrics(first), "cancel_release": max(0, ended-closed)*1000}
    finally:
        if response is not None:
            response.close()
        if conn is not None:
            conn.close()
        if sock is not None:
            sock.close()
        with STREAM_CONDITION:
            STREAM_TIMINGS.pop(identity, None)


def trial(runtime, upstream_port, count, concurrency, workload, *, binary=None, key_count=1, protocol="chat", client_websocket=False, metered=False, idle_websockets=0, go_procs=0, go_memory_limit=None, single_cpu=False, inputs=None):
    global STREAM_PEAK, STREAM_ABORTED
    with STREAM_CONDITION:
        STREAM_ABORTED = False
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
            command = [str(binary or ROOT / "bin/codex-lb"), "serve", "--self-update=false", "--listen", f"127.0.0.1:{port}", "--data-dir", directory, "--shutdown-grace", "0s"]
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
        idle_sockets = []
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
                if client_websocket:
                    settings, _ = request(port, "/api/settings", headers=admin)
                    request(port, "/api/settings", "PUT", {"expectedVersion": settings["version"], "upstreamStreamTransport": "websocket"}, admin)

                def respond(_index, warmup=False):
                    key = keys[_index % len(keys)]
                    if workload != "json":
                        phase = "warmup" if warmup and workload == "stream" else workload
                        size = inputs[_index] if inputs is not None and not warmup else PROMPT_BYTES
                        return streaming_request(port, key["key"], phase, protocol, client_websocket, size)
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
                with STREAM_CONDITION:
                    upstream_ws_before = UPSTREAM_WS_ACTIVE
                idle_phase = {}
                if idle_websockets:
                    for index in range(idle_websockets):
                        idle_sockets.append(open_idle_websocket(port, keys[index % len(keys)]["key"]))
                    cpu_open, _, fd_open, _, _ = counters(process.pid)
                    time.sleep(3)
                    cpu_idle, rss_idle, fd_idle, threads_idle, hwm_idle = counters(process.pid)
                    idle_phase = {"idle_websockets": len(idle_sockets), "idle_seconds": 3,
                                  "cpu_idle_setup_ms": round((cpu_open-cpu_before)*1000, 2),
                                  "cpu_idle_ms": round((cpu_idle-cpu_open)*1000, 2),
                                  "idle_server_cpu_percent": round((cpu_idle-cpu_open)/3*100, 2),
                                  "rss_idle_mib": round(rss_idle, 2), "fd_idle": fd_idle, "fd_idle_open": fd_open, "threads_idle": threads_idle}
                    for index, sock in enumerate(idle_sockets):
                        verify_idle_websocket(sock, index)
                    hwm_before = max(hwm_before, hwm_idle)
                    fd_before_for_peak = fd_idle
                    threads_before_for_peak = threads_idle
                else:
                    fd_before_for_peak, threads_before_for_peak = fd_before, threads_before
                peak = {"rss_mib": hwm_before, "fds": fd_before_for_peak, "threads": threads_before_for_peak}
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
                cpu_active_before = counters(process.pid)[0]
                before = time.perf_counter()
                failures = {}

                def measured_respond(index):
                    try:
                        if RAMP_SECONDS:
                            time.sleep(max(0, before + RAMP_SECONDS * index / max(1, count - 1) - time.perf_counter()))
                        return respond(index)
                    except Exception as error:
                        label = type(error).__name__ + ": " + str(error)
                        with STREAM_CONDITION:
                            failures[label] = failures.get(label, 0) + 1
                        raise

                try:
                    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
                        results = list(pool.map(measured_respond, range(count)))
                except Exception as error:
                    cpu, _, fds, _, hwm = counters(process.pid)
                    with STREAM_CONDITION:
                        active, maximum = STREAM_ACTIVE, STREAM_PEAK
                    print(json.dumps({"failed_trial": True, "error": str(error), "observed_errors": failures, "concurrency": concurrency,
                                      "prompt_bytes": PROMPT_BYTES, "idle_websockets": idle_websockets,
                                      "peak_rss_mib": round(max(peak["rss_mib"], hwm), 2),
                                      "peak_upstream_streams": maximum, "remaining_upstream_streams": active,
                                      "peak_fds": max(peak["fds"], fds), "cpu_ms": round((cpu-cpu_active_before)*1000, 2)}), flush=True)
                    raise
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
                output = {"runtime": runtime, "workload": workload, "protocol": protocol, "client_transport": "websocket" if client_websocket else "http", "requests": count, "concurrency": concurrency, "keys": key_count, "metered": metered, "errors": 0,
                        "startup_ms": round(startup_ms, 2), "cpu_ms": round((cpu_after-cpu_active_before)*1000, 2),
                        "rss_before_mib": round(rss_before, 2), "rss_after_mib": round(rss_after, 2),
                        "fd_before": fd_before, "fd_after": fd_after,
                        "threads_before": threads_before, "threads_after": threads_after,
                        "requests_per_second": round(count/elapsed, 2),
                        "server_cpu_percent": round((cpu_after-cpu_active_before)/elapsed*100, 2),
                        "gomaxprocs": go_procs or None, "gomemlimit": go_memory_limit, "prompt_bytes": PROMPT_BYTES, "stream_chunks": STREAM_CHUNKS,
                        "prompt_mix": {str(size): inputs.count(size) for size in sorted(set(inputs))} if inputs is not None else None,
                        "ramp_seconds": RAMP_SECONDS, "overlap_barrier": bool(REQUIRED_ACTIVE and not RAMP_SECONDS),
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
                if inputs is not None:
                    output["ttft_by_prompt_bytes"] = {}
                    for size in sorted(set(inputs)):
                        values = sorted(result["ttft"] for index, result in enumerate(results) if inputs[index] == size)
                        output["ttft_by_prompt_bytes"][str(size)] = {"p50_ms": round(statistics.median(values), 2),
                            "p95_ms": round(values[(len(values)*95+99)//100-1], 2)}
                if metered and workload != "cancel":
                    actual, _ = request(port, "/api/api-keys/", headers=admin)
                    by_id = {key["id"]: key for key in actual}
                    for index, key in enumerate(keys):
                        expected = 20 * (len(range(index, count, key_count)) + len(range(index, 8, key_count)))
                        if by_id[key["id"]]["limits"][0]["currentValue"] != expected:
                            raise RuntimeError("benchmark key accounting mismatch")
                    output["key_accounting_verified"] = True
                if idle_sockets:
                    for index, sock in enumerate(idle_sockets):
                        verify_idle_websocket(sock, index)
                    close_idle_websockets(idle_sockets)
                    idle_sockets = []
                    for _ in range(100):
                        cpu_closed, rss_closed, fd_closed, threads_closed, _ = counters(process.pid)
                        with STREAM_CONDITION:
                            retained_upstream = max(0, UPSTREAM_WS_ACTIVE - upstream_ws_before)
                        if fd_closed <= fd_before + retained_upstream + 16:
                            break
                        time.sleep(0.03)
                    else:
                        raise RuntimeError("server did not release idle WebSocket fds")
                    output.update({"idle_sockets_survived_workload": True, "rss_after_idle_close_mib": round(rss_closed, 2),
                                   "fd_after_idle_close": fd_closed, "threads_after_idle_close": threads_closed,
                                   "cpu_after_idle_close_ms": round((cpu_closed-cpu_after)*1000, 2)})
                output.update(idle_phase)
                if client_websocket and UPSTREAM_PROTOCOL == "responses":
                    with STREAM_CONDITION:
                        output["upstream_websockets_after_workload"] = UPSTREAM_WS_ACTIVE
                return output
            finally:
                with STREAM_CONDITION:
                    STREAM_ABORTED = True
                    STREAM_CONDITION.notify_all()
                close_idle_websockets(idle_sockets)
                if process.poll() is None:
                    process.terminate()
                    try:
                        process.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
                with STREAM_CONDITION:
                    if not STREAM_CONDITION.wait_for(lambda: UPSTREAM_WS_ACTIVE == 0, timeout=10):
                        raise RuntimeError("upstream WebSocket stubs remained open after runtime shutdown")


def prompt_sizes(mix, count):
    sizes = []
    for group in mix.split(","):
        size, amount = map(int, group.split(":"))
        if not 0 <= size <= 2*1024*1024 or not 1 <= amount <= count or len(sizes) + amount > count:
            raise ValueError("prompt mix sizes must be 0..2097152 and counts must total requests")
        sizes.extend([size] * amount)
    if len(sizes) != count:
        raise ValueError("prompt mix counts must total requests")
    random.Random(0).shuffle(sizes)
    return sizes


def main():
    global STREAM_CHUNKS, PROMPT_BYTES, REQUIRED_ACTIVE, UPSTREAM_PROTOCOL, RESPONSE_INPUT_ARRAY, RAMP_SECONDS
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runs", type=int, default=3)
    parser.add_argument("--requests", type=int, default=128)
    parser.add_argument("--concurrency", type=int, default=8)
    parser.add_argument("--workload", choices=("json", "stream", "cancel"), default="json")
    parser.add_argument("--protocol", choices=("chat", "responses"), default="chat")
    parser.add_argument("--client-websocket", action="store_true", help="send Responses streaming/cancel requests over a native client WebSocket")
    parser.add_argument("--upstream-protocol", choices=("chat", "responses"), default="chat")
    parser.add_argument("--input-array", action="store_true", help="use Codex-style Responses input messages instead of scalar text")
    parser.add_argument("--require-active", type=int, default=0, help="require this many overlapping streams; without a ramp, hold them at first event")
    parser.add_argument("--keys", type=int, default=1)
    parser.add_argument("--idle-websockets", type=int, default=0, help="hold authenticated native WebSockets without response.create")
    parser.add_argument("--metered", action="store_true", help="exercise and verify independent per-key token limits")
    parser.add_argument("--stream-chunks", type=int, default=5, help="20ms per chunk; use larger counts for sustained overlap")
    parser.add_argument("--go-binary", type=Path, default=ROOT / "bin/codex-lb")
    parser.add_argument("--go-procs", type=int, default=0, help="GOMAXPROCS for Go processes; 0 uses runtime default")
    parser.add_argument("--single-cpu", action="store_true", help="pin only the server process to one available Linux CPU")
    parser.add_argument("--go-memory-limit", help="optional standard Go GOMEMLIMIT, e.g. 96MiB")
    parser.add_argument("--prompt-bytes", type=int, default=0, help="append this many ASCII bytes to each request prompt")
    parser.add_argument("--prompt-mix", help="deterministically shuffled bytes:count groups, e.g. 32768:410,262144:92,1048576:10")
    parser.add_argument("--ramp-seconds", type=float, default=0, help="spread starts across this interval, without an artificial overlap barrier")
    parser.add_argument("--baseline-binary", type=Path)
    parser.add_argument("--runtimes", nargs="+", choices=("legacy", "go", "baseline"), default=["legacy", "go"])
    args = parser.parse_args()
    if min(args.runs, args.requests, args.concurrency, args.keys, args.stream_chunks) < 1 or args.runs > 10 or args.requests > 10000 or args.concurrency > 512 or args.keys > 256 or args.stream_chunks > 3000 or not 0 <= args.idle_websockets <= 4096:
        parser.error("expected runs 1..10, requests 1..10000, concurrency 1..512, keys 1..256, stream-chunks 1..3000, idle-websockets 0..4096")
    if "baseline" in args.runtimes and args.baseline_binary is None:
        parser.error("baseline runtime requires --baseline-binary")
    if not 0 <= args.require_active <= min(args.requests, args.concurrency) or args.require_active and args.workload != "stream":
        parser.error("require-active needs stream workload and must fit requests/concurrency")
    if args.require_active and (args.requests != args.concurrency or args.require_active != args.concurrency):
        parser.error("require-active measures one full concurrent wave: requests, concurrency and require-active must match")
    if args.upstream_protocol == "responses" and args.protocol != "responses":
        parser.error("Responses upstream requires Responses client protocol")
    if args.client_websocket and (args.protocol != "responses" or args.workload == "json"):
        parser.error("client-websocket needs Responses stream/cancel")
    if not 0 <= args.go_procs <= 128 or not 0 <= args.prompt_bytes <= 2*1024*1024:
        parser.error("expected go-procs 0..128 and prompt-bytes 0..2097152")
    if not 0 <= args.ramp_seconds <= 60 or args.ramp_seconds and args.requests != args.concurrency:
        parser.error("ramp-seconds must be 0..60 and needs one concurrent wave")
    if (args.prompt_mix or args.ramp_seconds) and args.workload != "stream":
        parser.error("prompt-mix and ramp-seconds need stream workload")
    inputs = None
    if args.prompt_mix:
        if args.prompt_bytes:
            parser.error("prompt-mix and prompt-bytes are mutually exclusive")
        try:
            inputs = prompt_sizes(args.prompt_mix, args.requests)
        except ValueError as error:
            parser.error(str(error))
    STREAM_CHUNKS = args.stream_chunks
    PROMPT_BYTES = args.prompt_bytes
    REQUIRED_ACTIVE, UPSTREAM_PROTOCOL = args.require_active, args.upstream_protocol
    RESPONSE_INPUT_ARRAY = args.input_array
    RAMP_SECONDS = args.ramp_seconds
    upstream = StubServer(("127.0.0.1", 0), Stub)
    worker = threading.Thread(target=upstream.serve_forever, daemon=True)
    worker.start()
    try:
        for run in range(args.runs):
            # Alternate order to avoid always giving one runtime a warmer host.
            for runtime in (args.runtimes if run % 2 == 0 else list(reversed(args.runtimes))):
                result = trial(runtime, upstream.server_port, args.requests, args.concurrency, args.workload,
                               binary=(args.baseline_binary if runtime == "baseline" else args.go_binary).resolve(),
                               key_count=args.keys, protocol=args.protocol, client_websocket=args.client_websocket, metered=args.metered,
                               idle_websockets=args.idle_websockets,
                               go_procs=args.go_procs, go_memory_limit=args.go_memory_limit, single_cpu=args.single_cpu, inputs=inputs)
                print(json.dumps({"run": run+1, **result}), flush=True)
    finally:
        upstream.shutdown()
        upstream.server_close()
        worker.join()


if __name__ == "__main__":
    main()
