"""mem AI Worker — Processor + Provider service.

This package implements the Python side of the mem AI pipeline:

  * mem_worker.server         — gRPC server entrypoint
  * mem_worker.processors.*   — per-mime processors (Image, Text, PDF, Audio)
  * mem_worker.providers.*    — model provider adapters (Ollama, OpenAI, Anthropic)
  * mem_worker.storage.*      — object-storage fetchers (S3)
  * mem_worker.config         — environment-driven settings

See SPEC.md §3 (F2 / F8) and §9 for the source-of-truth contract.
"""

from __future__ import annotations

__version__ = "0.1.0"
