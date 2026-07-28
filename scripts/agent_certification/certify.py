#!/usr/bin/env python3
"""Certify mem-mcp independently of any hosted model or user configuration.

The default commands are hermetic:

* ``fixtures`` validates the five checked-in host manifests and config files.
* ``contract`` starts a standard-library fake memd, drives the current
  ``mem-mcp`` binary over newline-delimited MCP stdio, and exercises the
  model-independent memory lifecycle and failure contract.
* ``real-hosts`` is opt-in. It executes installed host CLIs only under
  host-specific temporary configuration roots and prints sanitized evidence.

The runner targets documented host-specific temporary roots. A host without a
safe, host-specific isolation mechanism is reported ``NOT RUN``.
"""

from __future__ import annotations

import argparse
import contextlib
import copy
import dataclasses
import datetime as dt
import http.server
import json
import os
import platform
import queue
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
from pathlib import Path
from typing import Any, Iterable

try:
    import tomllib
except ModuleNotFoundError:  # Python 3.9/3.10 on supported macOS builders.
    tomllib = None  # type: ignore[assignment]


SCRIPT_DIR = Path(__file__).resolve().parent
REPOSITORY_ROOT = SCRIPT_DIR.parents[1]
FIXTURE_DIR = SCRIPT_DIR / "fixtures"
MANIFEST_DIR = SCRIPT_DIR / "manifests"
CHECKED_REPORT = REPOSITORY_ROOT / "docs/integrations/agent-host-certification.json"

PROTOCOL_VERSION = "2024-11-05"
WORKSPACE_A = "11111111-1111-4111-8111-111111111111"
WORKSPACE_B = "22222222-2222-4222-8222-222222222222"
MEMORY_ID = "33333333-3333-4333-8333-333333333333"
WRITE_TOKEN = "cert-write-token"
OTHER_TOKEN = "cert-other-token"
READ_ONLY_TOKEN = "cert-read-only-token"
INVALID_TOKEN = "cert-invalid-token"
HOST_IDS = ("openclaw", "hermes", "claude-code", "opencode", "codex")
STATUS_ORDER = {"NOT RUN": 0, "REGISTERED": 1, "DISCOVERED": 2, "INVOKED": 3}
REQUIRED_TOOLS = {
    "mem_remember",
    "mem_memory_get",
    "mem_feedback",
    "mem_archive",
    "mem_restore",
    "mem_forget",
    "mem_search",
    "mem_context",
}
SECRET_MARKERS = (WRITE_TOKEN, OTHER_TOKEN, READ_ONLY_TOKEN, INVALID_TOKEN)
MAX_COMMAND_OUTPUT_BYTES = 64 * 1024


class CertificationError(RuntimeError):
    """A failed certification assertion."""


class CertificationTimeout(CertificationError):
    """A bounded subprocess or protocol operation timed out."""


def require(condition: bool, message: str) -> None:
    if not condition:
        raise CertificationError(message)


def _json_bytes(value: Any) -> bytes:
    return json.dumps(value, separators=(",", ":"), sort_keys=True).encode("utf-8")


def _now() -> str:
    return "2026-07-28T00:00:00Z"


def _safe_process_env(extra: dict[str, str] | None = None) -> dict[str, str]:
    """Return a small environment instead of forwarding ambient credentials."""

    allowed = (
        "PATH",
        "LANG",
        "LC_ALL",
        "TMPDIR",
        "SHELL",
        "USER",
        "LOGNAME",
        "SystemRoot",
        "WINDIR",
        "PATHEXT",
    )
    env = {key: os.environ[key] for key in allowed if key in os.environ}
    if extra:
        env.update({key: str(value) for key, value in extra.items()})
    return env


class FakeMemdHandler(http.server.BaseHTTPRequestHandler):
    server: "FakeMemdHTTPServer"
    protocol_version = "HTTP/1.1"

    def log_message(self, _format: str, *_args: Any) -> None:
        return

    def _reply(self, status: int, value: Any) -> None:
        body = _json_bytes(value)
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        with contextlib.suppress(BrokenPipeError, ConnectionResetError):
            self.wfile.write(body)

    def _reply_malformed(self) -> None:
        body = b'{"broken":'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Connection", "close")
        self.end_headers()
        with contextlib.suppress(BrokenPipeError, ConnectionResetError):
            self.wfile.write(body)

    def _body(self) -> dict[str, Any]:
        size = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(size) if size else b"{}"
        try:
            value = json.loads(raw)
        except json.JSONDecodeError:
            self._reply(400, {"error": "invalid_json", "hint": "send valid JSON"})
            raise CertificationError("fake memd received malformed request JSON")
        if not isinstance(value, dict):
            self._reply(400, {"error": "invalid_request", "hint": "send an object"})
            raise CertificationError("fake memd request body was not an object")
        return value

    def _auth(self, *, write: bool) -> tuple[str, str] | None:
        auth = self.headers.get("Authorization", "")
        token = auth.removeprefix("Bearer ") if auth.startswith("Bearer ") else ""
        identities = {
            WRITE_TOKEN: (WORKSPACE_A, "write"),
            OTHER_TOKEN: (WORKSPACE_B, "write"),
            READ_ONLY_TOKEN: (WORKSPACE_A, "read"),
        }
        identity = identities.get(token)
        if identity is None:
            self._reply(
                401,
                {
                    "error": "invalid_token",
                    "hint": "set MEM_TOKEN to a valid Agent token",
                },
            )
            return None
        workspace, role = identity
        requested = self.headers.get("X-Workspace-ID", "")
        if requested and requested != workspace:
            self._reply(404, {"error": "not_found", "hint": "resource not found"})
            return None
        if write and role != "write":
            self._reply(
                403,
                {
                    "error": "insufficient_role",
                    "hint": "this operation requires memory write access",
                },
            )
            return None
        return workspace, role

    def _record_for(self, memory_id: str, workspace: str) -> dict[str, Any] | None:
        with self.server.state_lock:
            record = self.server.memories.get(memory_id)
            if (
                record is None
                or record["workspace_id"] != workspace
                or record.get("forgotten_at")
            ):
                return None
            return record

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        parsed = urllib.parse.urlsplit(self.path)
        match = re.fullmatch(r"/v1/memories/([0-9a-f-]+)", parsed.path)
        if match:
            identity = self._auth(write=False)
            if identity is None:
                return
            record = self._record_for(match.group(1), identity[0])
            if record is None:
                self._reply(404, {"error": "not_found", "hint": "resource not found"})
                return
            self._reply(200, _memory_detail(record))
            return
        self._reply(404, {"error": "not_found", "hint": "endpoint not found"})

    def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        parsed = urllib.parse.urlsplit(self.path)
        if parsed.path == "/v1/memories":
            self._remember()
            return
        if parsed.path == "/v1/search":
            self._search()
            return
        if parsed.path == "/v1/context":
            self._context()
            return
        match = re.fullmatch(
            r"/v1/memories/([0-9a-f-]+)/(feedback|archive|restore|forget)",
            parsed.path,
        )
        if match:
            self._mutate(match.group(1), match.group(2))
            return
        self._reply(404, {"error": "not_found", "hint": "endpoint not found"})

    def _remember(self) -> None:
        identity = self._auth(write=True)
        if identity is None:
            return
        body = self._body()
        key = self.headers.get("Idempotency-Key", "")
        if not key:
            self._reply(
                400,
                {"error": "idempotency_key_required", "hint": "send Idempotency-Key"},
            )
            return
        record = {
            "id": MEMORY_ID,
            "workspace_id": identity[0],
            "kind": body.get("kind", "observation"),
            "content": body.get("content", ""),
            "attributes": body.get("attributes", {}),
            "path": body.get("path", "/"),
            "event_at": body.get("event_at"),
            "source_type": body.get("source", {}).get("type", "agent"),
            "source_ref": body.get("source", {}).get("ref", "agent-certification"),
            "source_locator": body.get("source", {}).get("locator", {}),
            "producer_agent": body.get("producer", {}).get("agent_id", "certifier"),
            "producer_session": body.get("producer", {}).get(
                "session_id", "certifier-session"
            ),
            "producer_task": body.get("producer", {}).get(
                "task_id", "issue-46"
            ),
            "content_sha256": "a" * 64,
            "lifecycle_status": "active",
            "state_version": 1,
            "pinned": False,
            "useful_count": 0,
            "not_useful_count": 0,
            "feedback_score": 0,
            "feedback_count": 0,
            "created_at": _now(),
            "updated_at": _now(),
        }
        with self.server.state_lock:
            existing = self.server.idempotency.get((identity[0], key))
            if existing:
                self._reply(200, {**_memory_detail(existing), "replayed": True})
                return
            self.server.memories[MEMORY_ID] = record
            self.server.idempotency[(identity[0], key)] = record
            self.server.requests.append(("remember", identity[0]))
        self._reply(201, {**_memory_detail(record), "replayed": False})

    def _search(self) -> None:
        identity = self._auth(write=False)
        if identity is None:
            return
        body = self._body()
        query = body.get("query")
        if query == "__malformed__":
            self._reply_malformed()
            return
        if query == "__slow__":
            time.sleep(2)
        with self.server.state_lock:
            self.server.requests.append(("search", identity[0]))
            records = [
                record
                for record in self.server.memories.values()
                if record["workspace_id"] == identity[0]
                and record["lifecycle_status"] == "active"
                and not record.get("forgotten_at")
            ]
        results = [
            {
                "id": record["id"],
                "name": "structured-memory",
                "snippet": record["content"],
                "score": 0.99,
                "source_kind": "memory",
                "source_id": record["id"],
            }
            for record in records
        ]
        self._reply(200, {"query": query, "results": results})

    def _context(self) -> None:
        identity = self._auth(write=False)
        if identity is None:
            return
        body = self._body()
        with self.server.state_lock:
            self.server.requests.append(("context", identity[0]))
            records = [
                record
                for record in self.server.memories.values()
                if record["workspace_id"] == identity[0]
                and record["lifecycle_status"] == "active"
                and not record.get("forgotten_at")
            ]
        evidence = []
        for record in records[:1]:
            evidence.append(
                {
                    "evidence_id": "memory:" + record["id"],
                    "source_kind": "memory",
                    "source_id": record["id"],
                    "memory_id": record["id"],
                    "memory_kind": record["kind"],
                    "citation": "mem://memories/" + record["id"],
                    "content_sha256": record["content_sha256"],
                    "locator": record["source_locator"],
                    "excerpt": record["content"],
                    "score": 0.99,
                    "reason": "lexical memory match",
                    "provenance": {
                        "source_type": record["source_type"],
                        "source_ref": record["source_ref"],
                        "producer_agent": record["producer_agent"],
                    },
                }
            )
        partial = body.get("query") == "__partial__"
        warnings = (
            [
                {
                    "source": "file",
                    "code": "lane_unavailable",
                    "message": "file lane unavailable; memory evidence is complete",
                }
            ]
            if partial
            else []
        )
        self._reply(
            200,
            {
                "source": body.get("source", "all"),
                "evidence": evidence,
                "total_chars": sum(len(item["excerpt"]) for item in evidence),
                "partial": partial,
                "warnings": warnings,
                "retrieved_at": _now(),
            },
        )

    def _mutate(self, memory_id: str, action: str) -> None:
        identity = self._auth(write=True)
        if identity is None:
            return
        body = self._body()
        key = self.headers.get("Idempotency-Key", "")
        if not key:
            self._reply(
                400,
                {"error": "idempotency_key_required", "hint": "send Idempotency-Key"},
            )
            return
        record = self._record_for(memory_id, identity[0])
        if record is None:
            self._reply(404, {"error": "not_found", "hint": "resource not found"})
            return
        expected = body.get("expected_version")
        if expected != record["state_version"]:
            self._reply(
                409,
                {"error": "version_conflict", "hint": "refresh state_version"},
            )
            return
        if action == "feedback":
            if body.get("action") == "useful":
                record["useful_count"] += 1
                record["feedback_score"] += 1
            record["feedback_count"] += 1
        elif action == "archive":
            record["lifecycle_status"] = "archived"
        elif action == "restore":
            record["lifecycle_status"] = "active"
        elif action == "forget":
            record["lifecycle_status"] = "forgotten"
            record["forgotten_at"] = _now()
            record["content"] = ""
            record["path"] = ""
        record["state_version"] += 1
        record["updated_at"] = _now()
        with self.server.state_lock:
            self.server.requests.append((action, identity[0]))
        if action == "forget":
            self._reply(
                200,
                {
                    "tombstone": {
                        "id": record["id"],
                        "state_version": record["state_version"],
                        "forgotten_at": record["forgotten_at"],
                    },
                    "event": {"type": "memory.forgotten"},
                    "replayed": False,
                },
            )
            return
        self._reply(
            200,
            {
                "memory": _memory_control(record),
                "event": {"type": "memory." + action},
                "replayed": False,
            },
        )


class FakeMemdHTTPServer(http.server.ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = False

    def __init__(self) -> None:
        super().__init__(("127.0.0.1", 0), FakeMemdHandler)
        self.memories: dict[str, dict[str, Any]] = {}
        self.idempotency: dict[tuple[str, str], dict[str, Any]] = {}
        self.requests: list[tuple[str, str]] = []
        self.state_lock = threading.Lock()


class FakeMemd:
    def __init__(self) -> None:
        self.server = FakeMemdHTTPServer()
        self.thread = threading.Thread(
            target=self.server.serve_forever,
            name="fake-memd",
            daemon=True,
        )

    @property
    def url(self) -> str:
        host, port = self.server.server_address
        return f"http://{host}:{port}"

    def __enter__(self) -> "FakeMemd":
        self.thread.start()
        return self

    def __exit__(self, *_exc: Any) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=3)
        require(not self.thread.is_alive(), "fake memd did not stop")


def _memory_control(record: dict[str, Any]) -> dict[str, Any]:
    keys = (
        "id",
        "lifecycle_status",
        "state_version",
        "pinned",
        "useful_count",
        "not_useful_count",
        "feedback_score",
        "feedback_count",
        "updated_at",
    )
    return {key: record[key] for key in keys}


def _memory_detail(record: dict[str, Any]) -> dict[str, Any]:
    value = copy.deepcopy(record)
    value["citation"] = "mem://memories/" + record["id"]
    value["provenance"] = {
        "workspace_id": record["workspace_id"],
        "source_type": record["source_type"],
        "source_ref": record["source_ref"],
        "source_locator": record["source_locator"],
        "producer_agent": record["producer_agent"],
        "producer_session": record["producer_session"],
        "producer_task": record["producer_task"],
    }
    return value


class AdapterProcess:
    """Bounded newline-delimited JSON-RPC client for one mem-mcp process."""

    def __init__(
        self,
        binary: Path,
        *,
        server_url: str,
        token: str | None,
        workspace: str | None,
        response_timeout: float = 3.0,
        cwd: Path | None = None,
        args: list[str] | None = None,
        process_env: dict[str, str] | None = None,
    ) -> None:
        extra = {"MEM_SERVER": server_url}
        if token is not None:
            extra["MEM_TOKEN"] = token
        if workspace is not None:
            extra["MEM_WORKSPACE"] = workspace
        if process_env:
            extra.update(process_env)
        self.response_timeout = response_timeout
        self.process = subprocess.Popen(
            [str(binary), *(args or [])],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            cwd=str(cwd) if cwd else None,
            env=_safe_process_env(extra),
            start_new_session=True,
        )
        self.pid = self.process.pid
        self._next_id = 1
        self.stdout_lines: list[bytes] = []
        self.stderr_lines: list[bytes] = []
        self._stdout_queue: queue.Queue[bytes | None] = queue.Queue()
        self._stdout_thread = threading.Thread(
            target=self._read_lines,
            args=(self.process.stdout, self.stdout_lines, self._stdout_queue),
            daemon=True,
        )
        self._stderr_thread = threading.Thread(
            target=self._read_lines,
            args=(self.process.stderr, self.stderr_lines, None),
            daemon=True,
        )
        self._stdout_thread.start()
        self._stderr_thread.start()

    @staticmethod
    def _read_lines(
        stream: Any,
        destination: list[bytes],
        output_queue: queue.Queue[bytes | None] | None,
    ) -> None:
        if stream is None:
            if output_queue is not None:
                output_queue.put(None)
            return
        for line in iter(stream.readline, b""):
            destination.append(line)
            if output_queue is not None:
                output_queue.put(line)
        if output_queue is not None:
            output_queue.put(None)

    def _send(self, value: dict[str, Any]) -> None:
        if self.process.stdin is None or self.process.poll() is not None:
            raise CertificationError(
                f"mem-mcp exited before request (exit={self.process.poll()})"
            )
        self.process.stdin.write(_json_bytes(value) + b"\n")
        self.process.stdin.flush()

    def request(
        self,
        method: str,
        params: dict[str, Any] | None = None,
        *,
        timeout: float | None = None,
    ) -> dict[str, Any]:
        request_id = self._next_id
        self._next_id += 1
        message: dict[str, Any] = {
            "jsonrpc": "2.0",
            "id": request_id,
            "method": method,
        }
        if params is not None:
            message["params"] = params
        self._send(message)
        deadline = time.monotonic() + (
            self.response_timeout if timeout is None else timeout
        )
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                self.close()
                raise CertificationTimeout(f"timed out waiting for {method}")
            try:
                line = self._stdout_queue.get(timeout=remaining)
            except queue.Empty:
                self.close()
                raise CertificationTimeout(f"timed out waiting for {method}") from None
            if line is None:
                raise CertificationError(
                    f"mem-mcp closed stdout while waiting for {method}; "
                    f"exit={self.process.poll()}"
                )
            try:
                response = json.loads(line)
            except json.JSONDecodeError as exc:
                raise CertificationError(
                    f"mem-mcp polluted stdout with non-JSON: {line!r}"
                ) from exc
            require(
                isinstance(response, dict),
                f"{method} received a non-object JSON-RPC frame",
            )
            decoded = line.decode("utf-8", errors="replace")
            require(
                not any(secret in decoded for secret in SECRET_MARKERS),
                f"{method} response exposed a certification token",
            )
            require(
                response.get("jsonrpc") == "2.0",
                f"{method} response omitted JSON-RPC 2.0",
            )
            if "id" not in response:
                require(
                    isinstance(response.get("method"), str)
                    and "result" not in response
                    and "error" not in response,
                    f"{method} received an invalid JSON-RPC notification",
                )
                continue
            require(
                response.get("id") == request_id,
                f"{method} received foreign response id {response.get('id')!r}",
            )
            require(
                "method" not in response
                and (("result" in response) != ("error" in response)),
                f"{method} response must contain exactly one of result or error",
            )
            return response

    def notify(self, method: str, params: dict[str, Any] | None = None) -> None:
        message: dict[str, Any] = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            message["params"] = params
        self._send(message)

    def initialize(self) -> set[str]:
        response = self.request(
            "initialize",
            {
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {},
                "clientInfo": {"name": "mem-agent-certifier", "version": "1"},
            },
        )
        require("error" not in response, f"initialize failed: {response}")
        result = response.get("result", {})
        require(
            result.get("protocolVersion") == PROTOCOL_VERSION,
            "initialize negotiated an unexpected MCP revision",
        )
        require(
            result.get("serverInfo", {}).get("name") == "mem-mcp",
            "initialize returned the wrong server identity",
        )
        self.notify("notifications/initialized")
        listed = self.request("tools/list", {})
        require("error" not in listed, f"tools/list failed: {listed}")
        tools = listed.get("result", {}).get("tools", [])
        names = {tool.get("name") for tool in tools}
        require(REQUIRED_TOOLS <= names, f"tools/list missing {REQUIRED_TOOLS - names}")
        return names

    def call(
        self,
        name: str,
        arguments: dict[str, Any],
        *,
        timeout: float | None = None,
    ) -> dict[str, Any]:
        response = self.request(
            "tools/call",
            {"name": name, "arguments": arguments},
            timeout=timeout,
        )
        require("error" not in response, f"{name} returned protocol error: {response}")
        result = response.get("result")
        require(isinstance(result, dict), f"{name} omitted its MCP result")
        return result

    def close(self) -> None:
        if self.process.stdin is not None and not self.process.stdin.closed:
            with contextlib.suppress(BrokenPipeError):
                self.process.stdin.close()
        if self.process.poll() is None:
            try:
                self.process.wait(timeout=0.5)
            except subprocess.TimeoutExpired:
                if os.name == "posix" and self.pid > 1:
                    with contextlib.suppress(ProcessLookupError):
                        os.killpg(self.pid, signal.SIGTERM)
                else:
                    self.process.terminate()
                try:
                    self.process.wait(timeout=1)
                except subprocess.TimeoutExpired:
                    if os.name == "posix" and self.pid > 1:
                        with contextlib.suppress(ProcessLookupError):
                            os.killpg(self.pid, signal.SIGKILL)
                    else:
                        self.process.kill()
                    self.process.wait(timeout=2)
        self._stdout_thread.join(timeout=1)
        self._stderr_thread.join(timeout=1)
        for stream in (self.process.stdout, self.process.stderr):
            if stream is not None and not stream.closed:
                stream.close()

    def assert_clean(self) -> None:
        for raw in self.stdout_lines:
            decoded = raw.decode("utf-8", errors="replace")
            require(
                not any(secret in decoded for secret in SECRET_MARKERS),
                "mem-mcp stdout exposed a certification token",
            )
            try:
                value = json.loads(raw)
            except json.JSONDecodeError as exc:
                raise CertificationError(
                    f"mem-mcp stdout was not protocol-only: {raw!r}"
                ) from exc
            require(isinstance(value, dict), "mem-mcp stdout JSON was not an object")
            require(
                value.get("jsonrpc") == "2.0",
                "mem-mcp stdout frame omitted JSON-RPC 2.0",
            )
            require(
                (
                    "id" in value
                    and "method" not in value
                    and (("result" in value) != ("error" in value))
                )
                or (
                    "id" not in value
                    and isinstance(value.get("method"), str)
                    and "result" not in value
                    and "error" not in value
                ),
                "mem-mcp stdout contained an invalid JSON-RPC frame",
            )
        stderr = b"".join(self.stderr_lines).decode("utf-8", errors="replace")
        for secret in SECRET_MARKERS:
            require(secret not in stderr, "mem-mcp stderr exposed a certification token")
        require(self.process.poll() is not None, "mem-mcp child survived cleanup")

    def __enter__(self) -> "AdapterProcess":
        return self

    def __exit__(self, *_exc: Any) -> None:
        self.close()
        self.assert_clean()


def tool_json(result: dict[str, Any]) -> dict[str, Any]:
    require(result.get("isError") is False, f"tool failed: {result}")
    content = result.get("content")
    require(isinstance(content, list) and content, "tool returned no content")
    text = content[0].get("text")
    require(isinstance(text, str), "tool result did not contain text")
    value = json.loads(text)
    require(isinstance(value, dict), "tool text did not contain a JSON object")
    return value


def require_tool_error(result: dict[str, Any], marker: str) -> None:
    require(result.get("isError") is True, f"expected tool failure, got {result}")
    text = " ".join(
        block.get("text", "")
        for block in result.get("content", [])
        if isinstance(block, dict)
    )
    require(marker.lower() in text.lower(), f"tool error omitted {marker!r}: {text}")


def validate_fixtures() -> list[dict[str, Any]]:
    manifests = []
    for host_id in HOST_IDS:
        manifest_path = MANIFEST_DIR / f"{host_id}.json"
        require(manifest_path.is_file(), f"missing manifest: {manifest_path}")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        require(manifest.get("schema_version") == 1, f"{host_id}: schema version")
        require(manifest.get("host_id") == host_id, f"{host_id}: wrong host id")
        require(manifest.get("transport") == "stdio", f"{host_id}: not stdio")
        for key in (
            "scope",
            "command_shape",
            "environment_shape",
            "version",
            "status",
            "evidence",
            "real_probe",
        ):
            require(key in manifest, f"{host_id}: manifest missing {key}")
        require(
            manifest["status"] in STATUS_ORDER,
            f"{host_id}: invalid status {manifest['status']}",
        )
        fixture_path = SCRIPT_DIR / manifest["fixture"]
        require(fixture_path.is_file(), f"{host_id}: missing fixture")
        if fixture_path.suffix == ".toml":
            fixture = _parse_codex_toml(fixture_path.read_text(encoding="utf-8"))
        else:
            # The Hermes fixture is canonical JSON, which is a strict YAML subset.
            fixture = json.loads(fixture_path.read_text(encoding="utf-8"))
        server = _server_config(host_id, fixture)
        _validate_server_config(host_id, server)
        serialized = fixture_path.read_text(encoding="utf-8")
        require(
            not any(secret in serialized for secret in SECRET_MARKERS),
            f"{host_id}: fixture contains a token",
        )
        require(
            "/Users/" not in serialized and "/home/" not in serialized,
            f"{host_id}: fixture contains a private absolute path",
        )
        evidence_url = manifest["evidence"].get("official_docs", "")
        require(
            evidence_url.startswith("https://"),
            f"{host_id}: official evidence must be HTTPS",
        )
        manifests.append(manifest)
    _validate_checked_report({item["host_id"]: item for item in manifests})
    return manifests


def _validate_checked_report(manifests: dict[str, dict[str, Any]]) -> None:
    require(CHECKED_REPORT.is_file(), "missing checked Agent-host report")
    source = CHECKED_REPORT.read_text(encoding="utf-8")
    require(
        not any(secret in source for secret in SECRET_MARKERS),
        "checked Agent-host report contains a certification token",
    )
    require(
        "/Users/" not in source and "/home/" not in source,
        "checked Agent-host report contains a private path",
    )
    report = json.loads(source)
    require(report.get("schema_version") == 1, "host report schema version")
    hosts = report.get("hosts")
    require(
        isinstance(hosts, dict) and set(hosts) == set(HOST_IDS),
        "host report must contain exactly the five certified host rows",
    )
    for host_id, row in hosts.items():
        require(isinstance(row, dict), f"{host_id}: report row is not an object")
        for field in (
            "observed_at",
            "platform",
            "transport",
            "auth_method",
            "tested_operations",
            "result",
            "status",
            "evidence",
        ):
            require(field in row, f"{host_id}: report row missing {field}")
        require(row["transport"] == "stdio", f"{host_id}: report transport")
        require(
            row["auth_method"] == "MEM_TOKEN bearer via environment",
            f"{host_id}: report auth method",
        )
        require(
            isinstance(row["tested_operations"], list),
            f"{host_id}: tested_operations must be an array",
        )
        platform_row = row["platform"]
        require(
            isinstance(platform_row, dict)
            and isinstance(platform_row.get("os"), str)
            and isinstance(platform_row.get("arch"), str),
            f"{host_id}: invalid platform evidence",
        )
        evidence = row["evidence"]
        require(
            isinstance(evidence, dict)
            and evidence.get("manifest")
            == f"../../scripts/agent_certification/manifests/{host_id}.json"
            and evidence.get("official_docs")
            == manifests[host_id]["evidence"]["official_docs"]
            and evidence.get("report_anchor") == f"#/hosts/{host_id}",
            f"{host_id}: invalid evidence links",
        )
        status = row["status"]
        require(status in STATUS_ORDER, f"{host_id}: invalid report status")
        expected_result = {
            "NOT RUN": "NOT RUN",
            "REGISTERED": "PARTIAL",
            "DISCOVERED": "PARTIAL",
            "INVOKED": "PASS",
        }[status]
        require(
            row["result"] == expected_result,
            f"{host_id}: report result does not match status",
        )
    require(
        report.get("claims", {}).get("only_invoked_is_pass") is True,
        "host report must reserve PASS for INVOKED evidence",
    )


def _parse_codex_toml(source: str) -> dict[str, Any]:
    if tomllib is not None:
        parsed = tomllib.loads(source)
    else:
        # The certification fixture intentionally uses this small TOML subset
        # so macOS's system Python can validate it without installing packages.
        section: dict[str, Any] = {}
        matched_section = False
        for line_number, raw in enumerate(source.splitlines(), 1):
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            if line == "[mcp_servers.mem]":
                matched_section = True
                continue
            require(
                matched_section,
                f"codex TOML line {line_number}: unknown section",
            )
            match = re.fullmatch(r"([a-z_]+)\s*=\s*(.+)", line)
            require(
                match is not None,
                f"codex TOML line {line_number}: invalid syntax",
            )
            key, encoded = match.groups()
            if encoded.startswith('"'):
                value = json.loads(encoded)
            elif encoded.startswith("["):
                value = json.loads(encoded)
            elif re.fullmatch(r"\d+", encoded):
                value = int(encoded)
            else:
                raise CertificationError(
                    f"codex TOML line {line_number}: unsupported value"
                )
            section[key] = value
        require(matched_section, "codex TOML omitted [mcp_servers.mem]")
        parsed = {"mcp_servers": {"mem": section}}

    require(
        isinstance(parsed, dict) and set(parsed) == {"mcp_servers"},
        "codex TOML must contain only [mcp_servers.mem]",
    )
    servers = parsed.get("mcp_servers")
    require(
        isinstance(servers, dict) and set(servers) == {"mem"},
        "codex TOML must contain exactly one mem server",
    )
    server = servers["mem"]
    allowed = {
        "command",
        "args",
        "env_vars",
        "startup_timeout_sec",
        "tool_timeout_sec",
    }
    require(
        isinstance(server, dict) and set(server) <= allowed,
        "codex TOML contains unsupported mem server keys",
    )
    require(
        {"command", "args", "env_vars"} <= set(server),
        "codex TOML is missing command, args, or env_vars",
    )
    return parsed


def _server_config(host_id: str, fixture: dict[str, Any]) -> dict[str, Any]:
    if host_id == "openclaw":
        return fixture["mcp"]["servers"]["mem"]
    if host_id == "hermes":
        return fixture["mcp_servers"]["mem"]
    if host_id == "claude-code":
        return fixture["mcpServers"]["mem"]
    if host_id == "opencode":
        return fixture["mcp"]["mem"]
    if host_id == "codex":
        return fixture["mcp_servers"]["mem"]
    raise CertificationError(f"unknown host {host_id}")


def _validate_server_config(host_id: str, server: dict[str, Any]) -> None:
    require(isinstance(server, dict), f"{host_id}: server config is not an object")
    if host_id == "opencode":
        require(server.get("type") == "local", "opencode: local type is required")
        command = server.get("command")
        require(
            isinstance(command, list) and command,
            "opencode: command must be a non-empty argv array",
        )
        env = server.get("environment")
    else:
        command = server.get("command")
        require(isinstance(command, str) and command, f"{host_id}: command is required")
        env = server.get("env")
    if host_id == "codex":
        require(
            set(server.get("env_vars", []))
            == {"MEM_SERVER", "MEM_TOKEN", "MEM_WORKSPACE"},
            "codex: env_vars must explicitly forward the three mem variables",
        )
        return
    require(isinstance(env, dict), f"{host_id}: environment mapping is required")
    require(
        set(env) >= {"MEM_SERVER", "MEM_TOKEN", "MEM_WORKSPACE"},
        f"{host_id}: environment mapping is incomplete",
    )


def _adapter_for(
    binary: Path,
    fake: FakeMemd,
    *,
    token: str | None = WRITE_TOKEN,
    workspace: str | None = WORKSPACE_A,
    timeout: float = 3.0,
) -> AdapterProcess:
    return AdapterProcess(
        binary,
        server_url=fake.url,
        token=token,
        workspace=workspace,
        response_timeout=timeout,
    )


def run_contract(binary: Path) -> dict[str, Any]:
    binary = binary.resolve()
    require(binary.is_file(), f"mem-mcp binary not found: {binary}")
    require(os.access(binary, os.X_OK), f"mem-mcp binary is not executable: {binary}")
    manifests = validate_fixtures()
    checks: list[str] = ["fixtures"]

    with FakeMemd() as fake:
        with _adapter_for(binary, fake) as client:
            names = client.initialize()
            checks.append("initialize-list")
            remembered = tool_json(
                client.call(
                    "mem_remember",
                    {
                        "content": "Agent host certification memory",
                        "kind": "decision",
                        "path": "/Projects/mem",
                        "idempotency_key": "cert-remember-1",
                        "source_ref": "issue-46",
                        "agent_id": "certifier",
                        "session_id": "certifier-session",
                        "task_id": "issue-46",
                    },
                )
            )
            require(remembered["id"] == MEMORY_ID, "remember returned wrong identity")
            require(
                remembered["workspace_id"] == WORKSPACE_A,
                "remember returned wrong workspace",
            )
            checks.append("authenticated-remember")

            searched = tool_json(
                client.call("mem_search", {"query": "host certification"})
            )
            require(
                searched["results"][0]["id"] == MEMORY_ID,
                "search did not recall the memory",
            )
            checks.append("search-recall")

            context = tool_json(
                client.call(
                    "mem_context",
                    {"query": "host certification", "source": "memory"},
                )
            )
            evidence = context["evidence"][0]
            require(
                evidence["source_kind"] == "memory"
                and evidence["source_id"] == MEMORY_ID
                and evidence["memory_id"] == MEMORY_ID,
                "context lost source identity",
            )
            require(
                evidence["citation"] == f"mem://memories/{MEMORY_ID}",
                "context returned an unstable citation",
            )
            checks.append("context-source-identity")

            partial = tool_json(
                client.call(
                    "mem_context",
                    {"query": "__partial__", "source": "all"},
                )
            )
            require(
                partial["partial"] is True and partial["warnings"],
                "partial context was not explicit",
            )
            require(partial["evidence"], "partial context discarded usable evidence")
            checks.append("partial-context")

            with _adapter_for(
                binary, fake, token=OTHER_TOKEN, workspace=WORKSPACE_B
            ) as other:
                other.initialize()
                denied = other.call("mem_memory_get", {"memory_id": MEMORY_ID})
                require_tool_error(denied, "not_found")
                other_search = tool_json(
                    other.call("mem_search", {"query": "host certification"})
                )
                require(
                    other_search["results"] == [],
                    "another workspace recalled the active memory",
                )
            checks.append("cross-workspace-denial-active")

            feedback = tool_json(
                client.call(
                    "mem_feedback",
                    {
                        "memory_id": MEMORY_ID,
                        "action": "useful",
                        "expected_version": 1,
                        "idempotency_key": "cert-feedback-1",
                    },
                )
            )
            require(
                feedback["memory"]["state_version"] == 2,
                "feedback did not update state",
            )
            archived = tool_json(
                client.call(
                    "mem_archive",
                    {
                        "memory_id": MEMORY_ID,
                        "expected_version": 2,
                        "idempotency_key": "cert-archive-1",
                    },
                )
            )
            require(
                archived["memory"]["lifecycle_status"] == "archived",
                "archive did not update lifecycle",
            )
            require(
                tool_json(client.call("mem_search", {"query": "host certification"}))[
                    "results"
                ]
                == [],
                "archived memory remained recallable",
            )
            restored = tool_json(
                client.call(
                    "mem_restore",
                    {
                        "memory_id": MEMORY_ID,
                        "expected_version": 3,
                        "idempotency_key": "cert-restore-1",
                    },
                )
            )
            require(
                restored["memory"]["lifecycle_status"] == "active",
                "restore did not update lifecycle",
            )
            checks.append("update-archive-restore")

            malformed = client.call("mem_search", {"query": "__malformed__"})
            require(malformed.get("isError") is True, "malformed response succeeded")
            malformed_text = " ".join(
                block.get("text", "")
                for block in malformed.get("content", [])
                if isinstance(block, dict)
            ).lower()
            require(
                "unexpected eof" in malformed_text
                or "invalid character" in malformed_text,
                f"malformed response error was not diagnostic: {malformed_text}",
            )
            unknown = client.call("mem_certification_unknown", {})
            require_tool_error(unknown, "unknown tool")
            checks.extend(("malformed-upstream", "unknown-tool"))

            forgotten = tool_json(
                client.call(
                    "mem_forget",
                    {
                        "memory_id": MEMORY_ID,
                        "expected_version": 4,
                        "reason": "user_request",
                        "idempotency_key": "cert-forget-1",
                        "confirm": True,
                    },
                )
            )
            require(
                forgotten["tombstone"]["state_version"] == 5,
                "forget did not advance the tombstone",
            )
            require_tool_error(
                client.call("mem_memory_get", {"memory_id": MEMORY_ID}),
                "not_found",
            )
            checks.append("delete-forget-owner-denial")

        with _adapter_for(
            binary, fake, token=INVALID_TOKEN, workspace=WORKSPACE_A
        ) as invalid:
            invalid.initialize()
            require_tool_error(
                invalid.call("mem_search", {"query": "certification"}),
                "invalid_token",
            )
            checks.append("invalid-token")

        with _adapter_for(
            binary, fake, token=READ_ONLY_TOKEN, workspace=WORKSPACE_A
        ) as read_only:
            read_only.initialize()
            require_tool_error(
                read_only.call(
                    "mem_remember",
                    {
                        "content": "must fail",
                        "kind": "note",
                        "path": "/",
                        "idempotency_key": "must-fail",
                    },
                ),
                "insufficient_role",
            )
            checks.append("insufficient-role")

        with _adapter_for(binary, fake, token=None, workspace=WORKSPACE_A) as missing:
            missing.initialize()
            require_tool_error(
                missing.call("mem_search", {"query": "certification"}),
                "invalid_token",
            )
            missing.close()
            stderr = b"".join(missing.stderr_lines).decode(
                "utf-8", errors="replace"
            )
            require("no token supplied" in stderr, "missing-token warning absent")
            checks.append("missing-token")

        unavailable_port = _unused_loopback_port()
        with AdapterProcess(
            binary,
            server_url=f"http://127.0.0.1:{unavailable_port}",
            token=WRITE_TOKEN,
            workspace=WORKSPACE_A,
        ) as unavailable:
            unavailable.initialize()
            failure = unavailable.call("mem_search", {"query": "certification"})
            require_tool_error(failure, "connect")
            checks.append("unavailable-server")

        timed = _adapter_for(binary, fake, timeout=0.2)
        timed.initialize()
        timed_pid = timed.pid
        try:
            timed.call("mem_search", {"query": "__slow__"}, timeout=0.2)
            raise CertificationError("slow upstream did not trigger client timeout")
        except CertificationTimeout:
            pass
        finally:
            timed.close()
            timed.assert_clean()
        require(not _pid_alive(timed_pid), "timed-out mem-mcp survived cleanup")
        checks.append("timeout-cleanup")

        with tempfile.TemporaryDirectory(prefix="mem-agent-cert-space-") as raw:
            spaced_dir = Path(raw) / "path with spaces"
            spaced_dir.mkdir()
            spaced_binary = spaced_dir / "mem mcp"
            try:
                spaced_binary.symlink_to(binary)
            except OSError:
                shutil.copy2(binary, spaced_binary)
                spaced_binary.chmod(0o700)
            with _adapter_for(spaced_binary, fake) as spaced:
                spaced.initialize()
                probe = tool_json(
                    spaced.call("mem_search", {"query": "path with spaces"})
                )
                require("results" in probe, "spaced command path was not invoked")
            checks.append("path-with-spaces")

        # Run the same safe call through every fixture shape. This is a
        # manifest harness result, not evidence that an external host ran.
        for manifest in manifests:
            with _adapter_for(binary, fake) as fixture_client:
                fixture_client.initialize()
                probe = tool_json(
                    fixture_client.call(
                        "mem_search",
                        {"query": f"fixture-{manifest['host_id']}"},
                    )
                )
                require("results" in probe, "fixture harness call failed")
            checks.append(f"fixture-harness:{manifest['host_id']}")

    return {
        "schema_version": 1,
        "result": "PASS",
        "protocol_version": PROTOCOL_VERSION,
        "transport": "stdio",
        "host_fixture_count": len(manifests),
        "checks": checks,
    }


def _unused_loopback_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _pid_alive(pid: int) -> bool:
    if pid <= 1:
        return False
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    proc_stat = Path(f"/proc/{pid}/stat")
    if proc_stat.is_file():
        with contextlib.suppress(OSError, IndexError):
            # A killed child can remain as a non-running zombie until the
            # container's PID 1 reaps it. That is not a surviving process.
            if proc_stat.read_text(encoding="utf-8").split()[2] == "Z":
                return False
    if os.name == "posix":
        with contextlib.suppress(OSError, subprocess.SubprocessError):
            state = subprocess.run(
                ["ps", "-o", "stat=", "-p", str(pid)],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                text=True,
                timeout=1,
                check=False,
            ).stdout.strip()
            if state.startswith("Z"):
                return False
    return True


@dataclasses.dataclass
class CommandEvidence:
    name: str
    argv: list[str]
    exit_code: int | None
    outcome: str
    proves: str
    output: str
    validated: bool = False
    validation_error: str = ""


def _run_bounded(
    argv: list[str],
    *,
    env: dict[str, str],
    cwd: Path,
    name: str,
    proves: str,
    timeout: float = 15,
) -> CommandEvidence:
    for argument in argv:
        require(
            not any(secret in argument for secret in SECRET_MARKERS),
            f"{name}: secret material must never appear in process argv",
        )
    process: subprocess.Popen[bytes] | None = None
    try:
        process = subprocess.Popen(
            argv,
            cwd=cwd,
            env=env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        output_chunks: list[bytes] = []
        output_size = 0
        output_truncated = False
        output_lock = threading.Lock()

        def drain_output() -> None:
            nonlocal output_size, output_truncated
            assert process is not None and process.stdout is not None
            for chunk in iter(lambda: process.stdout.read(4096), b""):
                with output_lock:
                    output_chunks.append(chunk)
                    output_size += len(chunk)
                    while output_size > MAX_COMMAND_OUTPUT_BYTES and output_chunks:
                        excess = output_size - MAX_COMMAND_OUTPUT_BYTES
                        first = output_chunks[0]
                        if len(first) <= excess:
                            output_chunks.pop(0)
                            output_size -= len(first)
                        else:
                            output_chunks[0] = first[excess:]
                            output_size -= excess
                        output_truncated = True

        output_thread = threading.Thread(target=drain_output, daemon=True)
        output_thread.start()
        try:
            process.wait(timeout=timeout)
            exit_code = process.returncode
            outcome = "PASS" if exit_code == 0 else "FAIL"
        except subprocess.TimeoutExpired:
            _terminate_process_group(process)
            with contextlib.suppress(subprocess.TimeoutExpired):
                process.wait(timeout=2)
            exit_code = None
            outcome = "TIMEOUT"
        _terminate_process_group(process)
        output_thread.join(timeout=2)
        if output_thread.is_alive() and process.stdout is not None:
            process.stdout.close()
            output_thread.join(timeout=1)
        with output_lock:
            output = b"".join(output_chunks).decode("utf-8", errors="replace")
            if output_truncated:
                output = "[earlier output truncated]\n" + output
        return CommandEvidence(
            name,
            argv,
            exit_code,
            outcome,
            proves,
            output,
        )
    finally:
        if process is not None:
            _terminate_process_group(process)
            if process.stdout is not None and not process.stdout.closed:
                process.stdout.close()


def _terminate_process_group(process: subprocess.Popen[Any]) -> None:
    """Reap a bounded command and every child it left in its new session."""

    if os.name != "posix":
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=1)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=2)
        return
    pid = process.pid
    if pid <= 1:
        return
    try:
        os.killpg(pid, 0)
    except ProcessLookupError:
        return
    with contextlib.suppress(ProcessLookupError):
        os.killpg(pid, signal.SIGTERM)
    deadline = time.monotonic() + 1
    while time.monotonic() < deadline:
        try:
            os.killpg(pid, 0)
        except ProcessLookupError:
            return
        time.sleep(0.02)
    with contextlib.suppress(ProcessLookupError):
        os.killpg(pid, signal.SIGKILL)
    if process.poll() is None:
        with contextlib.suppress(subprocess.TimeoutExpired):
            process.wait(timeout=2)


def _write_private_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")
    path.chmod(0o600)


def _sanitize_text(text: str, roots: Iterable[Path]) -> str:
    sanitized = text
    for secret in SECRET_MARKERS:
        sanitized = sanitized.replace(secret, "<REDACTED>")
    sanitized = re.sub(r"Bearer\s+\S+", "Bearer <REDACTED>", sanitized)
    sanitized = re.sub(r"https?://127\.0\.0\.1:\d+", "<LOOPBACK>", sanitized)
    for root in sorted((str(path) for path in roots), key=len, reverse=True):
        sanitized = sanitized.replace(root, "<TEMP>")
    sanitized = sanitized.replace(str(REPOSITORY_ROOT), "<WORKTREE>")
    sanitized = re.sub(
        r"/(?:Users|home)/[^/\s\"']+(?:/[^,\s\"']*)?",
        "<PRIVATE_PATH>",
        sanitized,
    )
    return sanitized[-8000:]


def _safe_argv(argv: list[str], roots: Iterable[Path]) -> list[str]:
    return [_sanitize_text(part, roots) for part in argv]


def _max_status(current: str, candidate: str) -> str:
    return candidate if STATUS_ORDER[candidate] > STATUS_ORDER[current] else current


def run_real_hosts(binary: Path) -> dict[str, Any]:
    """Run installed host registration/discovery probes under temporary roots."""

    binary = binary.resolve()
    require(binary.is_file(), f"mem-mcp binary not found: {binary}")
    manifests = {item["host_id"]: item for item in validate_fixtures()}
    rows: list[dict[str, Any]] = []
    observed_at = (
        dt.datetime.now(dt.timezone.utc)
        .replace(microsecond=0)
        .isoformat()
        .replace("+00:00", "Z")
    )
    platform_row = {
        "os": platform.system(),
        "release": platform.release(),
        "arch": platform.machine(),
    }
    with tempfile.TemporaryDirectory(prefix="mem-real-hosts-") as raw, FakeMemd() as fake:
        isolation_root = Path(raw)
        for host_id in HOST_IDS:
            manifest = manifests[host_id]
            executable = shutil.which(manifest["real_probe"]["binary"])
            row: dict[str, Any] = {
                "host_id": host_id,
                "status": "NOT RUN",
                "version": None,
                "result": "NOT RUN",
                "reason": "host executable is not installed",
                "observed_at": observed_at,
                "platform": platform_row,
                "transport": "stdio",
                "auth_method": "MEM_TOKEN bearer via environment",
                "tested_operations": [],
                "evidence": {
                    "manifest": (
                        "../../scripts/agent_certification/manifests/"
                        f"{host_id}.json"
                    ),
                    "official_docs": manifest["evidence"]["official_docs"],
                    "report_anchor": f"#/hosts/{host_id}",
                },
                "isolation": {
                    "status": "NOT VERIFIED",
                    "scope": manifest["scope"],
                },
                "commands": [],
            }
            if executable is None:
                rows.append(row)
                continue
            row["tested_operations"] = [
                "version",
                *[
                    command["name"]
                    for command in manifest["real_probe"]["commands"]
                ],
            ]
            host_root = isolation_root / host_id
            host_root.mkdir(mode=0o700)
            env = _safe_process_env(
                {
                    "MEM_SERVER": fake.url,
                    "MEM_TOKEN": WRITE_TOKEN,
                    "MEM_WORKSPACE": WORKSPACE_A,
                    "MEM_MCP_COMMAND": str(binary),
                }
            )
            argv_sets = _prepare_real_probe(
                host_id,
                Path(executable),
                manifest,
                host_root,
                env,
            )
            if host_id in {"openclaw", "claude-code", "opencode"}:
                row["isolation"]["status"] = "VERIFIED"
            status = "NOT RUN"
            evidence: list[CommandEvidence] = []
            for command in argv_sets:
                requests_before = len(fake.server.requests)
                item = _run_bounded(
                    command["argv"],
                    env=env,
                    cwd=command.get("cwd", host_root),
                    name=command["name"],
                    proves=command["proves"],
                    timeout=float(command.get("timeout", 15)),
                )
                requests_after = fake.server.requests[requests_before:]
                item.validated, item.validation_error = _validate_host_evidence(
                    host_id,
                    command,
                    item,
                    binary,
                    requests_after,
                )
                evidence.append(item)
                if item.outcome == "PASS" and item.validated:
                    status = _max_status(status, item.proves)
                if command["name"] == "version" and item.outcome == "PASS":
                    row["version"] = _sanitize_text(
                        item.output.strip().splitlines()[0][:200],
                        (isolation_root, binary.parent),
                    )
            row["status"] = status
            row["result"] = {
                "NOT RUN": "NOT RUN",
                "REGISTERED": "PARTIAL",
                "DISCOVERED": "PARTIAL",
                "INVOKED": "PASS",
            }[status]
            row["reason"] = (
                _real_host_reason(host_id, status, evidence)
                if status == "NOT RUN"
                else "highest directly observed evidence level"
            )
            row["commands"] = [
                {
                    **dataclasses.asdict(item),
                    "argv": _safe_argv(item.argv, (isolation_root, binary.parent)),
                    "output": _sanitize_text(
                        item.output, (isolation_root, binary.parent)
                    ),
                }
                for item in evidence
            ]
            rows.append(row)

    return {
        "schema_version": 1,
        "generated_at": observed_at,
        "platform": platform_row,
        "transport": "stdio",
        "isolation": "reported per host; no global isolation claim",
        "hosts": rows,
    }


def diagnose_opencode(binary: Path) -> dict[str, Any]:
    """Trace only method/id metadata through a real isolated OpenCode probe."""

    binary = binary.resolve()
    require(binary.is_file(), f"mem-mcp binary not found: {binary}")
    executable = shutil.which("opencode")
    require(executable is not None, "opencode is not installed")
    manifests = {item["host_id"]: item for item in validate_fixtures()}
    with tempfile.TemporaryDirectory(
        prefix="mem-opencode-diagnosis-"
    ) as raw, FakeMemd() as fake:
        root = Path(raw)
        fixture = json.loads(
            (SCRIPT_DIR / manifests["opencode"]["fixture"]).read_text(
                encoding="utf-8"
            )
        )
        trace_path = root / "trace.log"
        fixture["mcp"]["mem"]["command"] = [
            sys.executable,
            str(SCRIPT_DIR / "stdio_trace.py"),
            "--target",
            str(binary),
            "--log",
            str(trace_path),
        ]
        fixture["mcp"]["mem"]["timeout"] = 15000
        config_dir = root / "config"
        data_dir = root / "data"
        cache_dir = root / "cache"
        state_dir = root / "state"
        for path in (config_dir, data_dir, cache_dir, state_dir):
            path.mkdir()
        env = _safe_process_env(
            {
                "MEM_SERVER": fake.url,
                "MEM_TOKEN": WRITE_TOKEN,
                "MEM_WORKSPACE": WORKSPACE_A,
                "OPENCODE_CONFIG_CONTENT": json.dumps(fixture),
                "OPENCODE_CONFIG_DIR": str(config_dir),
                "OPENCODE_DB": str(data_dir / "opencode.db"),
                "XDG_CONFIG_HOME": str(config_dir),
                "XDG_DATA_HOME": str(data_dir),
                "XDG_CACHE_HOME": str(cache_dir),
                "XDG_STATE_HOME": str(state_dir),
            }
        )
        evidence = _run_bounded(
            [executable, "--pure", "mcp", "list"],
            env=env,
            cwd=root,
            name="opencode-diagnostic-list",
            proves="NOT RUN",
            timeout=20,
        )
        trace = (
            trace_path.read_text(encoding="utf-8")
            if trace_path.is_file()
            else "proxy not launched\n"
        )
        require(
            not any(secret in trace for secret in SECRET_MARKERS),
            "diagnostic trace exposed a token",
        )
        return {
            "schema_version": 1,
            "host_id": "opencode",
            "version": "1.17.9",
            "status": "NOT RUN",
            "command": {
                **dataclasses.asdict(evidence),
                "argv": _safe_argv(evidence.argv, (root, binary.parent)),
                "output": _sanitize_text(evidence.output, (root, binary.parent)),
            },
            "trace": trace.splitlines(),
            "adapter_launched": "proxy launched" in trace,
            "initialize_received": "method=initialize" in trace,
            "tools_list_received": "method=tools/list" in trace,
            "note": "diagnostic proxy is transparent but is not certification evidence",
        }


def _validate_host_evidence(
    host_id: str,
    command: dict[str, Any],
    item: CommandEvidence,
    binary: Path,
    request_delta: list[tuple[str, str]],
) -> tuple[bool, str]:
    if item.outcome != "PASS":
        return False, f"command outcome was {item.outcome}"
    if any(secret in item.output for secret in SECRET_MARKERS):
        return False, "command output exposed secret material"
    validator = command.get("validator")
    if command["name"] == "version":
        if not item.output.strip():
            return False, "version command returned empty output"
        return True, ""
    lowered = item.output.lower()
    if validator == "openclaw-registered":
        try:
            decoded = json.loads(item.output)
        except json.JSONDecodeError:
            return False, "OpenClaw list output was not JSON"
        lowered_json = json.dumps(decoded, sort_keys=True).lower()
        if re.search(
            r"\b(?:failed|error|disconnected)\b|not\s+(?:found|connected)|no\s+mcp",
            lowered_json,
        ):
            return False, "OpenClaw output reported a negative mem state"
        entry = _find_server_entry(decoded, "mem")
        if entry is None:
            return False, "OpenClaw output did not contain a structured mem entry"
        command_value = entry.get("command")
        if not isinstance(command_value, str) or binary.name not in command_value:
            return False, "OpenClaw mem entry did not identify the mem-mcp command"
        return True, ""
    if validator == "claude-registered":
        ansi_free = re.sub(r"\x1b\[[0-9;]*[A-Za-z]", "", lowered)
        mem_lines = [
            line
            for line in ansi_free.splitlines()
            if re.search(r"(?<![a-z])mem(?![a-z])", line)
        ]
        if not mem_lines:
            return False, "Claude list output did not identify mem"
        negative = re.compile(
            r"\b(?:failed|error|disconnected|pending|denied)\b"
            r"|not\s+(?:found|connected)|no\s+mcp"
        )
        for line in mem_lines:
            if negative.search(line):
                continue
            if binary.name.lower() in line and re.search(r"\bconnected\b", line):
                return True, ""
        return False, "Claude did not report the configured mem server connected"
    if validator == "opencode-discovered":
        ansi_free = re.sub(r"\x1b\[[0-9;]*[A-Za-z]", "", lowered)
        mem_lines = [
            line
            for line in ansi_free.splitlines()
            if re.search(r"(?<![a-z])mem(?![a-z])", line)
        ]
        if not mem_lines:
            return False, "OpenCode list output did not identify mem"
        negative = re.compile(r"\b(?:disconnected|failed|error)\b|not\s+connected")
        for line in mem_lines:
            if negative.search(line):
                continue
            if re.search(r"\bconnected\b", line):
                return True, ""
        return False, "OpenCode did not report the mem server as connected"
    if validator == "mem-search-invoked":
        if ("search", WORKSPACE_A) not in request_delta:
            return False, f"{host_id} produced no attributable mem_search request"
        return True, ""
    return False, f"no validator is defined for {command['name']}"


def _find_server_entry(value: Any, server_name: str) -> dict[str, Any] | None:
    if isinstance(value, dict):
        for identity_key in ("name", "id", "server"):
            if value.get(identity_key) == server_name:
                return value
        keyed = value.get(server_name)
        if isinstance(keyed, dict):
            return keyed
        for nested in value.values():
            found = _find_server_entry(nested, server_name)
            if found is not None:
                return found
    elif isinstance(value, list):
        for nested in value:
            found = _find_server_entry(nested, server_name)
            if found is not None:
                return found
    return None


def _real_host_reason(
    host_id: str, status: str, evidence: list[CommandEvidence]
) -> str:
    if host_id == "hermes":
        return "static schema only; no documented non-interactive custom-server probe"
    if not evidence:
        return "no command was eligible to run"
    failures = [item for item in evidence if item.outcome != "PASS"]
    if failures:
        item = failures[-1]
        code = "timeout" if item.exit_code is None else f"exit {item.exit_code}"
        return f"{item.name} did not complete successfully ({code})"
    invalid = [item for item in evidence if not item.validated]
    if invalid:
        item = invalid[-1]
        return f"{item.name} evidence rejected: {item.validation_error}"
    return f"no command advanced beyond {status}"


def _prepare_real_probe(
    host_id: str,
    executable: Path,
    manifest: dict[str, Any],
    host_root: Path,
    env: dict[str, str],
) -> list[dict[str, Any]]:
    fixture_path = SCRIPT_DIR / manifest["fixture"]
    commands: list[dict[str, Any]] = [
        {
            "name": "version",
            "argv": [str(executable), *manifest["real_probe"]["version_args"]],
            "proves": "NOT RUN",
            "timeout": 8,
        }
    ]

    if host_id == "openclaw":
        fixture = json.loads(fixture_path.read_text(encoding="utf-8"))
        config_path = host_root / "openclaw.json"
        state_dir = host_root / "state"
        state_dir.mkdir()
        _write_private_json(config_path, fixture)
        _assert_file_has_no_secret(config_path)
        env.update(
            {
                "OPENCLAW_CONFIG_PATH": str(config_path),
                "OPENCLAW_STATE_DIR": str(state_dir),
            }
        )
    elif host_id == "claude-code":
        fixture = json.loads(fixture_path.read_text(encoding="utf-8"))
        project_dir = host_root / "project"
        project_dir.mkdir()
        _write_private_json(project_dir / ".mcp.json", fixture)
        _assert_file_has_no_secret(project_dir / ".mcp.json")
        config_dir = host_root / "config"
        config_dir.mkdir()
        env["CLAUDE_CONFIG_DIR"] = str(config_dir)
        for command in manifest["real_probe"]["commands"]:
            commands.append(
                {
                    **command,
                    "argv": [str(executable), *command["args"]],
                    "cwd": project_dir,
                }
            )
        return commands
    elif host_id == "opencode":
        fixture = json.loads(fixture_path.read_text(encoding="utf-8"))
        config_dir = host_root / "config"
        data_dir = host_root / "data"
        cache_dir = host_root / "cache"
        state_dir = host_root / "state"
        for path in (config_dir, data_dir, cache_dir, state_dir):
            path.mkdir()
        env.update(
            {
                "OPENCODE_CONFIG_CONTENT": json.dumps(fixture),
                "OPENCODE_CONFIG_DIR": str(config_dir),
                "OPENCODE_DB": str(data_dir / "opencode.db"),
                "XDG_CONFIG_HOME": str(config_dir),
                "XDG_DATA_HOME": str(data_dir),
                "XDG_CACHE_HOME": str(cache_dir),
                "XDG_STATE_HOME": str(state_dir),
            }
        )
    elif host_id == "codex":
        # Codex has no host-specific config-root environment variable. Its
        # `-c` overrides still merge with user config, so a local probe cannot
        # prove that normal config was unread. Keep runtime status NOT RUN.
        return commands
    elif host_id == "hermes":
        # The pinned source documents runtime discovery but no bounded,
        # non-interactive custom-server list/probe command. Do not launch an
        # interactive Agent or turn a static parse into runtime evidence.
        return commands

    for command in manifest["real_probe"]["commands"]:
        commands.append(
            {
                **command,
                "argv": [str(executable), *command["args"]],
            }
        )
    return commands


def _assert_file_has_no_secret(path: Path) -> None:
    source = path.read_text(encoding="utf-8")
    require(
        not any(secret in source for secret in SECRET_MARKERS),
        f"temporary config {path.name} materialized secret content",
    )


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("fixtures", help="validate host manifests and config fixtures")
    contract = sub.add_parser("contract", help="run hermetic mem-mcp certification")
    contract.add_argument("--mcp-binary", required=True, type=Path)
    real = sub.add_parser(
        "real-hosts", help="opt-in isolated probes of installed host CLIs"
    )
    real.add_argument("--mcp-binary", required=True, type=Path)
    diagnostic = sub.add_parser(
        "diagnose-opencode",
        help="trace method/id metadata through an isolated OpenCode probe",
    )
    diagnostic.add_argument("--mcp-binary", required=True, type=Path)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _build_parser().parse_args(argv)
    try:
        if args.command == "fixtures":
            result: Any = {
                "schema_version": 1,
                "result": "PASS",
                "hosts": [item["host_id"] for item in validate_fixtures()],
            }
        elif args.command == "contract":
            result = run_contract(args.mcp_binary)
        elif args.command == "real-hosts":
            result = run_real_hosts(args.mcp_binary)
        else:
            result = diagnose_opencode(args.mcp_binary)
        print(json.dumps(result, indent=2, sort_keys=True))
        return 0
    except (CertificationError, OSError, ValueError) as exc:
        safe_error = _sanitize_text(str(exc), (Path.cwd(),))
        print(
            json.dumps(
                {"schema_version": 1, "result": "FAIL", "error": safe_error},
                sort_keys=True,
            ),
            file=sys.stderr,
        )
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
