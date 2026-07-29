"""Deterministic, model-free recall benchmark."""

from .runner import BenchmarkError, run_benchmark

__all__ = ["BenchmarkError", "run_benchmark"]
