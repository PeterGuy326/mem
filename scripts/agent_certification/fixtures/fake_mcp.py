#!/usr/bin/env python3
"""Tiny process fixture for testing the certification client's boundaries."""

import json
import os
import sys
import time


TOOLS = (
    "mem_remember",
    "mem_memory_get",
    "mem_feedback",
    "mem_archive",
    "mem_restore",
    "mem_forget",
    "mem_search",
    "mem_context",
)


def emit(value):
    sys.stdout.write(json.dumps(value, separators=(",", ":")) + "\n")
    sys.stdout.flush()


mode = os.environ.get("CERT_FAKE_MODE", "good")
for raw in sys.stdin:
    message = json.loads(raw)
    if mode == "polluted":
        sys.stdout.write("not-json\n")
        sys.stdout.flush()
        continue
    if mode == "foreign-id":
        emit({"jsonrpc": "2.0", "id": 999, "result": {}})
        continue
    if mode == "non-object":
        emit([])
        continue
    if mode == "invalid-frame":
        emit({"debug": "cert-write-" + "token"})
        continue
    if mode == "hang":
        time.sleep(60)
        continue
    if "id" not in message:
        continue
    method = message.get("method")
    if method == "initialize":
        result = {
            "protocolVersion": "2024-11-05",
            "capabilities": {"tools": {"listChanged": False}},
            "serverInfo": {"name": "mem-mcp", "version": "fixture"},
        }
    elif method == "tools/list":
        result = {
            "tools": [
                {
                    "name": name,
                    "description": "fixture",
                    "inputSchema": {"type": "object"},
                }
                for name in TOOLS
            ]
        }
    else:
        result = {
            "content": [{"type": "text", "text": '{"results":[]}'}],
            "isError": False,
        }
    emit({"jsonrpc": "2.0", "id": message["id"], "result": result})
