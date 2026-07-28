#!/usr/bin/env python3
"""Transparent stdio relay that records only JSON-RPC method/id metadata."""

from __future__ import annotations

import argparse
import contextlib
import json
import subprocess
import sys
import threading
from pathlib import Path
from typing import Any


KNOWN_METHODS = {
    "initialize",
    "notifications/initialized",
    "tools/list",
    "tools/call",
    "ping",
}


def _label(direction: str, raw: bytes) -> str:
    try:
        value: Any = json.loads(raw)
    except json.JSONDecodeError:
        return f"{direction} non-json bytes={len(raw)}"
    if not isinstance(value, dict):
        return f"{direction} non-object-json"
    if "method" in value:
        method = value.get("method")
        safe_method = method if method in KNOWN_METHODS else "<unknown>"
        id_state = "present" if "id" in value else "notification"
        return f"{direction} method={safe_method} id={id_state}"
    outcome = "error" if "error" in value else "result"
    id_state = "present" if "id" in value else "none"
    return f"{direction} {outcome} id={id_state}"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--target", required=True, type=Path)
    parser.add_argument("--log", required=True, type=Path)
    args = parser.parse_args()
    lock = threading.Lock()
    args.log.parent.mkdir(parents=True, exist_ok=True)

    def record(message: str) -> None:
        with lock:
            with args.log.open("a", encoding="utf-8") as stream:
                stream.write(message + "\n")

    record("proxy launched")
    child = subprocess.Popen(
        [str(args.target)],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )

    def relay_output() -> None:
        assert child.stdout is not None
        for raw in iter(child.stdout.readline, b""):
            record(_label("adapter->host", raw))
            sys.stdout.buffer.write(raw)
            sys.stdout.buffer.flush()

    def relay_stderr() -> None:
        assert child.stderr is not None
        for raw in iter(child.stderr.readline, b""):
            # Do not copy stderr text into the trace: it can include URLs or
            # future diagnostics. Its existence and byte count are sufficient.
            record(f"adapter-stderr bytes={len(raw)}")
            sys.stderr.buffer.write(raw)
            sys.stderr.buffer.flush()

    stdout_thread = threading.Thread(target=relay_output, daemon=True)
    stderr_thread = threading.Thread(target=relay_stderr, daemon=True)
    stdout_thread.start()
    stderr_thread.start()
    assert child.stdin is not None
    try:
        for raw in iter(sys.stdin.buffer.readline, b""):
            record(_label("host->adapter", raw))
            child.stdin.write(raw)
            child.stdin.flush()
    except BrokenPipeError:
        record("adapter stdin closed")
    finally:
        with contextlib.suppress(BrokenPipeError):
            child.stdin.close()
    exit_code = child.wait()
    stdout_thread.join(timeout=1)
    stderr_thread.join(timeout=1)
    record(f"adapter exit={exit_code}")
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
