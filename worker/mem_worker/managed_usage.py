"""Closed Worker-side managed-stage receipt vocabulary.

Processors use the private metadata key while they run. The gRPC boundary
removes it and emits only the public, bounded ``managed_usage`` receipt.
"""

from __future__ import annotations

CONTRACT = "mem.managed-stage-receipt/v1"
PROCESSOR_STAGES_KEY = "_managed_usage_stages"

NOT_INVOKED = "not_invoked"
SUCCEEDED = "succeeded"
INDETERMINATE = "indeterminate"
OUTCOMES = frozenset({NOT_INVOKED, SUCCEEDED, INDETERMINATE})


def is_managed_provider(spec: str | None) -> bool:
    if not spec:
        return False
    return spec.partition(":")[0].lower() not in {
        "ollama",
        "clip",
        "faster-whisper",
        "whisper",
    }


__all__ = [
    "CONTRACT",
    "INDETERMINATE",
    "NOT_INVOKED",
    "OUTCOMES",
    "PROCESSOR_STAGES_KEY",
    "SUCCEEDED",
    "is_managed_provider",
]
