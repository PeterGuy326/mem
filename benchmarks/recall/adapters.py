from __future__ import annotations

from collections import Counter
import json
import math
from pathlib import Path
import re
import unicodedata
from typing import Any, Mapping

from .dataset import Dataset, Document, Query, document_matches_filters
from .errors import BenchmarkError


RANKINGS_SCHEMA = "mem.recall-rankings.v1"
STATUS_VALUES = frozenset({"ok", "partial", "error"})
_CJK_SEGMENT = re.compile(r"[\u3400-\u4dbf\u4e00-\u9fff]+")
_WORD = re.compile(r"[^\W_]+", re.UNICODE)
_ERROR_CODE = re.compile(r"[a-z0-9][a-z0-9_.-]{0,63}")
_MAX_RANKINGS_BYTES = 8 * 1024 * 1024
_MAX_RESULTS_PER_QUERY = 1000
_SENSITIVE_KEYS = frozenset(
    {
        "api_key",
        "apikey",
        "authorization",
        "password",
        "secret",
        "token",
        "access_token",
        "credential",
        "credentials",
    }
)


def _tokens(text: str) -> list[str]:
    normalized = unicodedata.normalize("NFKC", text).casefold()
    tokens: list[str] = []
    cursor = 0
    for match in _CJK_SEGMENT.finditer(normalized):
        tokens.extend(_WORD.findall(normalized[cursor : match.start()]))
        segment = match.group(0)
        tokens.extend(segment)
        tokens.extend(
            segment[index : index + 2] for index in range(max(0, len(segment) - 1))
        )
        cursor = match.end()
    tokens.extend(_WORD.findall(normalized[cursor:]))
    return tokens


def _lexical_score(query: Query, document: Document, idf: Mapping[str, float]) -> float:
    query_tokens = Counter(_tokens(query.text))
    document_tokens = Counter(_tokens(document.text))
    score = sum(
        idf.get(token, 0.0)
        * min(query_count, document_tokens.get(token, 0))
        * (1.0 + math.log1p(document_tokens.get(token, 0)))
        for token, query_count in query_tokens.items()
    )
    normalized_query = unicodedata.normalize("NFKC", query.text).casefold()
    normalized_document = unicodedata.normalize("NFKC", document.text).casefold()
    if normalized_query in normalized_document:
        score += sum(idf.get(token, 0.0) for token in query_tokens) + 1.0
    return score


def lexical_rankings(dataset: Dataset) -> dict[str, Any]:
    query_rows: list[dict[str, Any]] = []
    for query in dataset.queries:
        eligible = [
            document
            for document in dataset.documents
            if document_matches_filters(document, query.filters)
        ]
        document_frequency: Counter[str] = Counter()
        for document in eligible:
            document_frequency.update(set(_tokens(document.text)))
        idf = {
            token: math.log((len(eligible) + 1) / (frequency + 1)) + 1.0
            for token, frequency in document_frequency.items()
        }
        scored = [
            (_lexical_score(query, document, idf), document) for document in eligible
        ]
        scored = [item for item in scored if item[0] > 0]
        scored.sort(key=lambda item: (-item[0], item[1].id))
        query_rows.append(
            {
                "query_id": query.id,
                "status": "ok",
                # A deterministic sentinel: this reference adapter validates quality,
                # not wall-clock performance. Candidate adapters supply measured time.
                "latency_ms": 0.0,
                "results": [
                    {
                        "doc_id": document.id,
                        "citation": document.citation,
                        "score": score,
                    }
                    for score, document in scored[:10]
                ],
            }
        )
    return {
        "schema_version": RANKINGS_SCHEMA,
        "engine": "lexical-reference",
        "configuration": {
            "mode": "lexical",
            "provider": None,
            "model": None,
            "dimension": None,
            "index": {
                "kind": "in-memory",
                "tokenizer": "unicode-words+cjk-unigrams+bigrams",
            },
            "search": {
                "kind": "deterministic-tfidf-overlap",
                "top_k": 10,
                "tie_break": "document_id_ascending",
            },
        },
        "hardware": {
            "timing_mode": "deterministic-zero",
            "note": "reference quality run; not a performance benchmark",
        },
        "queries": query_rows,
    }


def _validate_configuration(raw: Any) -> dict[str, Any]:
    if not isinstance(raw, dict):
        raise BenchmarkError("external rankings.configuration must be an object")
    required = ("mode", "provider", "model", "dimension", "index", "search")
    missing = [field for field in required if field not in raw]
    if missing:
        raise BenchmarkError(
            f"external rankings.configuration missing fields: {missing}"
        )
    mode = raw["mode"]
    if mode not in {"lexical", "vector", "hybrid"}:
        raise BenchmarkError(
            "external rankings.configuration.mode must be lexical, vector, or hybrid"
        )
    if mode == "lexical":
        if any(raw[field] is not None for field in ("provider", "model", "dimension")):
            raise BenchmarkError(
                "external lexical configuration must explicitly set provider, "
                "model, and dimension to null"
            )
    else:
        for field in ("provider", "model"):
            if not isinstance(raw[field], str) or not raw[field].strip():
                raise BenchmarkError(
                    f"external rankings.configuration.{field} must be non-empty"
                )
        dimension = raw["dimension"]
        if (
            not isinstance(dimension, int)
            or isinstance(dimension, bool)
            or dimension <= 0
        ):
            raise BenchmarkError(
                "external rankings.configuration.dimension must be a positive integer"
            )
    for field in ("index", "search"):
        if not isinstance(raw[field], dict) or not raw[field]:
            raise BenchmarkError(
                f"external rankings.configuration.{field} must be a non-empty object"
            )
    return dict(raw)


def _reject_sensitive_keys(value: Any, context: str) -> None:
    if isinstance(value, dict):
        for key, nested in value.items():
            normalized_key = str(key).casefold().replace("-", "_")
            if normalized_key in _SENSITIVE_KEYS:
                raise BenchmarkError(
                    f"{context} contains forbidden sensitive key: {key}"
                )
            _reject_sensitive_keys(nested, f"{context}.{key}")
    elif isinstance(value, list):
        for index, nested in enumerate(value):
            _reject_sensitive_keys(nested, f"{context}[{index}]")


def load_external_rankings(path: str | Path, dataset: Dataset) -> dict[str, Any]:
    rankings_path = Path(path)
    try:
        if rankings_path.stat().st_size > _MAX_RANKINGS_BYTES:
            raise BenchmarkError(f"rankings file exceeds {_MAX_RANKINGS_BYTES} bytes")
        raw = json.loads(rankings_path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise BenchmarkError(f"rankings file not found: {rankings_path}") from exc
    except json.JSONDecodeError as exc:
        raise BenchmarkError(
            f"invalid rankings JSON at line {exc.lineno}: {exc.msg}"
        ) from exc
    if not isinstance(raw, dict):
        raise BenchmarkError("external rankings must be an object")
    if raw.get("schema_version") != RANKINGS_SCHEMA:
        raise BenchmarkError(
            f"external rankings.schema_version must be {RANKINGS_SCHEMA!r}"
        )
    engine = raw.get("engine")
    if not isinstance(engine, str) or not engine.strip():
        raise BenchmarkError("external rankings.engine must be non-empty")
    raw_queries = raw.get("queries")
    if not isinstance(raw_queries, list):
        raise BenchmarkError("external rankings.queries must be an array")

    expected_query_ids = {query.id for query in dataset.queries}
    seen_query_ids: set[str] = set()
    validated_queries: list[dict[str, Any]] = []
    for index, row in enumerate(raw_queries, start=1):
        context = f"external rankings.queries[{index}]"
        if not isinstance(row, dict):
            raise BenchmarkError(f"{context} must be an object")
        query_id = row.get("query_id")
        if not isinstance(query_id, str) or not query_id:
            raise BenchmarkError(f"{context}.query_id must be non-empty")
        if query_id in seen_query_ids:
            raise BenchmarkError(f"duplicate external query_id: {query_id}")
        seen_query_ids.add(query_id)
        status = row.get("status")
        if status not in STATUS_VALUES:
            raise BenchmarkError(
                f"{context}.status must be one of {sorted(STATUS_VALUES)}"
            )
        latency = row.get("latency_ms")
        if (
            not isinstance(latency, (int, float))
            or isinstance(latency, bool)
            or not math.isfinite(float(latency))
            or latency < 0
        ):
            raise BenchmarkError(f"{context}.latency_ms must be finite and >= 0")
        raw_results = row.get("results")
        if not isinstance(raw_results, list):
            raise BenchmarkError(f"{context}.results must be an array")
        if len(raw_results) > _MAX_RESULTS_PER_QUERY:
            raise BenchmarkError(
                f"{context}.results exceeds {_MAX_RESULTS_PER_QUERY} entries"
            )
        if status == "error" and raw_results:
            raise BenchmarkError(f"{context}.results must be empty for error status")
        error_code = row.get("error_code")
        if error_code is not None and (
            not isinstance(error_code, str) or not _ERROR_CODE.fullmatch(error_code)
        ):
            raise BenchmarkError(
                f"{context}.error_code must match {_ERROR_CODE.pattern!r}"
            )
        results: list[dict[str, Any]] = []
        for result_index, result in enumerate(raw_results, start=1):
            result_context = f"{context}.results[{result_index}]"
            if not isinstance(result, dict):
                raise BenchmarkError(f"{result_context} must be an object")
            doc_id = result.get("doc_id")
            if not isinstance(doc_id, str) or not doc_id or len(doc_id) > 512:
                raise BenchmarkError(f"{result_context}.doc_id must be non-empty")
            validated: dict[str, Any] = {"doc_id": doc_id}
            if "citation" in result:
                if (
                    not isinstance(result["citation"], str)
                    or len(result["citation"]) > 2048
                ):
                    raise BenchmarkError(
                        f"{result_context}.citation must be a string <= 2048 chars"
                    )
                validated["citation"] = result["citation"]
            if "score" in result:
                score = result["score"]
                if (
                    not isinstance(score, (int, float))
                    or isinstance(score, bool)
                    or not math.isfinite(float(score))
                ):
                    raise BenchmarkError(
                        f"{result_context}.score must be a finite number"
                    )
                validated["score"] = float(score)
            results.append(validated)
        validated_queries.append(
            {
                "query_id": query_id,
                "status": status,
                "latency_ms": float(latency),
                "results": results,
                **({"error_code": error_code} if error_code is not None else {}),
            }
        )

    if seen_query_ids != expected_query_ids:
        missing = sorted(expected_query_ids.difference(seen_query_ids))
        extra = sorted(seen_query_ids.difference(expected_query_ids))
        raise BenchmarkError(
            f"external rankings query mismatch: missing={missing}, extra={extra}"
        )

    configuration = _validate_configuration(raw.get("configuration"))
    hardware = raw.get("hardware")
    if not isinstance(hardware, dict) or not hardware:
        raise BenchmarkError("external rankings.hardware must be a non-empty object")
    _reject_sensitive_keys(configuration, "external rankings.configuration")
    _reject_sensitive_keys(hardware, "external rankings.hardware")
    return {
        "schema_version": RANKINGS_SCHEMA,
        "engine": engine,
        "configuration": configuration,
        "hardware": hardware,
        "queries": validated_queries,
    }
