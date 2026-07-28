#!/usr/bin/env python3
"""Spawn a same-process-group child, print its PID, and exit immediately."""

import subprocess
import sys


child = subprocess.Popen(
    [sys.executable, "-c", "import time; time.sleep(60)"],
    stdin=subprocess.DEVNULL,
    stdout=subprocess.DEVNULL,
    stderr=subprocess.DEVNULL,
)
print(child.pid, flush=True)
