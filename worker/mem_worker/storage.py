"""Storage URI fetcher.

Translates ``storage_uri`` from a :class:`ProcessRequest` into raw bytes.

Supported schemes:
    * ``s3://<bucket>/<key>``     — S3 / MinIO (uses ``MEM_S3_*`` env)
    * ``file:///abs/path``        — local filesystem (handy for tests)
    * ``http://`` / ``https://``  — anonymous GET (for ``mem put --url`` echos)

The S3 client is constructed lazily so the worker can start with no S3
configured (HealthCheck still returns SERVING).
"""

from __future__ import annotations

import base64
from functools import lru_cache
from typing import Optional
from urllib.parse import urlparse

import requests

from .config import get_settings
from .logging import get_logger

log = get_logger(__name__)


class StorageError(Exception):
    """Raised when ``fetch_bytes`` cannot resolve a URI."""


def fetch_bytes(uri: str, *, timeout: float = 30.0) -> bytes:
    """Return the raw bytes pointed to by ``uri``.

    Raises:
        StorageError: bad scheme, missing creds, network error, 4xx/5xx.
    """
    if not uri:
        raise StorageError("empty storage_uri")

    parsed = urlparse(uri)
    scheme = parsed.scheme.lower()

    if scheme == "s3":
        return _fetch_s3(parsed.netloc, parsed.path.lstrip("/"))
    if scheme == "file":
        return _fetch_file(parsed.path)
    if scheme in ("http", "https"):
        return _fetch_http(uri, timeout=timeout)
    if scheme == "data":
        return _fetch_data(uri)

    raise StorageError(f"unsupported scheme {scheme!r} in {uri!r}")


# ---------------------------------------------------------------------------
# S3
# ---------------------------------------------------------------------------

@lru_cache(maxsize=1)
def _s3_client():
    """Lazily build a boto3 S3 client from ``MEM_S3_*`` settings."""
    try:
        import boto3  # noqa: WPS433 — lazy import, boto3 is heavy
        from botocore.config import Config as BotoConfig
    except ImportError as exc:  # pragma: no cover — boto3 is a hard dep
        raise StorageError(f"boto3 not installed: {exc}") from exc

    s = get_settings()
    if not s.s3_endpoint:
        raise StorageError("MEM_S3_ENDPOINT not configured")
    return boto3.client(
        "s3",
        endpoint_url=s.s3_endpoint,
        aws_access_key_id=s.s3_access_key,
        aws_secret_access_key=s.s3_secret_key,
        region_name=s.s3_region,
        use_ssl=s.s3_use_ssl,
        config=BotoConfig(signature_version="s3v4"),
    )


def _fetch_s3(bucket: str, key: str) -> bytes:
    if not bucket or not key:
        raise StorageError(f"malformed s3 uri: bucket={bucket!r} key={key!r}")
    try:
        resp = _s3_client().get_object(Bucket=bucket, Key=key)
    except Exception as exc:  # boto3 raises a zoo of ClientError types
        raise StorageError(f"s3 get_object failed: {exc}") from exc
    body = resp.get("Body")
    if body is None:
        raise StorageError("s3 response missing Body")
    return body.read()


# ---------------------------------------------------------------------------
# file://
# ---------------------------------------------------------------------------

def _fetch_file(path: str) -> bytes:
    if not path:
        raise StorageError("empty file:// path")
    try:
        with open(path, "rb") as fh:
            return fh.read()
    except OSError as exc:
        raise StorageError(f"open {path}: {exc}") from exc


# ---------------------------------------------------------------------------
# http(s)://
# ---------------------------------------------------------------------------

def _fetch_http(uri: str, *, timeout: float) -> bytes:
    try:
        resp = requests.get(uri, timeout=timeout)
        resp.raise_for_status()
    except requests.RequestException as exc:
        raise StorageError(f"http get {uri}: {exc}") from exc
    return resp.content


# ---------------------------------------------------------------------------
# data: (RFC 2397)
# ---------------------------------------------------------------------------

def _fetch_data(uri: str) -> bytes:
    """Decode a ``data:<mime>[;base64],<payload>`` URI.

    Used by query-time embedding (the Go workerclient packs short query text
    here so we avoid round-tripping through S3 for ephemeral data).
    """
    head, _, payload = uri.partition(",")
    if not head.startswith("data:"):
        raise StorageError(f"malformed data uri: {uri[:32]!r}")
    if ";base64" in head:
        try:
            return base64.b64decode(payload)
        except (ValueError, base64.binascii.Error) as exc:
            raise StorageError(f"data uri base64 decode failed: {exc}") from exc
    return payload.encode("utf-8")


# Test seam: reset cached S3 client (e.g. when env changes between tests).
def _reset_cache() -> None:  # pragma: no cover
    _s3_client.cache_clear()


__all__ = ["StorageError", "fetch_bytes"]
