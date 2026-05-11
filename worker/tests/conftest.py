"""Shared pytest fixtures and PYTHONPATH bootstrap."""

from __future__ import annotations

import os
import sys
from pathlib import Path

# Ensure ``import mem_worker`` works whether or not the package is installed.
_ROOT = Path(__file__).resolve().parents[1]
if str(_ROOT) not in sys.path:
    sys.path.insert(0, str(_ROOT))

# Reset settings cache before each test session so env tweaks land cleanly.
import pytest  # noqa: E402


@pytest.fixture(autouse=True)
def _reset_settings_cache():
    from mem_worker.config import get_settings

    get_settings.cache_clear()
    yield
    get_settings.cache_clear()


@pytest.fixture
def env(monkeypatch):
    """Set env vars and ensure get_settings re-reads."""
    def _set(**kwargs):
        for k, v in kwargs.items():
            monkeypatch.setenv(k, str(v))
        from mem_worker.config import get_settings

        get_settings.cache_clear()

    return _set
