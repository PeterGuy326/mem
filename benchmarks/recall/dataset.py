from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import math
from pathlib import Path
from typing import Any, Mapping

from .errors import BenchmarkError


REQUIRED_SLICES = frozenset({"exact", "paraphrase", "filters", "hard-negative"})
REQUIRED_LANGUAGES = frozenset({"en", "zh"})
REQUIRED_SOURCE_KINDS = frozenset({"structured", "text", "image_caption"})
ALLOWED_FILTERS = frozenset({"workspace", "path_prefix", "source_kind", "metadata"})


@dataclass(frozen=True)
class Document:
    id: str
    language: str
    source_kind: str
    workspace: str
    path: str
    citation: str
    text: str
    metadata: Mapping[str, Any]


@dataclass(frozen=True)
class Query:
    id: str
    text: str
    language: str
    slice: str
    filters: Mapping[str, Any]
    expected_source_kind: str


@dataclass(frozen=True)
class Dataset:
    root: Path
    metadata: Mapping[str, Any]
    documents: tuple[Document, ...]
    queries: tuple[Query, ...]
    qrels: Mapping[str, Mapping[str, float]]
    checksum: str

    @property
    def documents_by_id(self) -> dict[str, Document]:
        return {document.id: document for document in self.documents}


def _read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise BenchmarkError(f"missing dataset file: {path.name}") from exc
    except json.JSONDecodeError as exc:
        raise BenchmarkError(
            f"invalid JSON in {path.name} at line {exc.lineno}: {exc.msg}"
        ) from exc


def _read_jsonl(path: Path) -> list[Mapping[str, Any]]:
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except FileNotFoundError as exc:
        raise BenchmarkError(f"missing dataset file: {path.name}") from exc
    records: list[Mapping[str, Any]] = []
    for line_number, line in enumerate(lines, start=1):
        if not line.strip():
            continue
        try:
            record = json.loads(line)
        except json.JSONDecodeError as exc:
            raise BenchmarkError(
                f"invalid JSON in {path.name}:{line_number}: {exc.msg}"
            ) from exc
        if not isinstance(record, dict):
            raise BenchmarkError(f"{path.name}:{line_number} must be an object")
        records.append(record)
    return records


def _required_string(record: Mapping[str, Any], field: str, context: str) -> str:
    value = record.get(field)
    if not isinstance(value, str) or not value.strip():
        raise BenchmarkError(f"{context}.{field} must be a non-empty string")
    return value


def _is_clean_absolute_path(value: str, *, allow_trailing_slash: bool) -> bool:
    if not value.startswith("/"):
        return False
    if allow_trailing_slash and value != "/" and value.endswith("//"):
        return False
    normalized = value.rstrip("/") if allow_trailing_slash and value != "/" else value
    if normalized == "/":
        return True
    if normalized != "/" and normalized.endswith("/"):
        return False
    segments = normalized.split("/")[1:]
    return all(segment not in {"", ".", ".."} for segment in segments)


def _canonical_checksum(
    metadata: Mapping[str, Any],
    corpus: list[Mapping[str, Any]],
    queries: list[Mapping[str, Any]],
    qrels: Mapping[str, Any],
) -> str:
    payload = json.dumps(
        {
            "metadata": metadata,
            "corpus": corpus,
            "queries": queries,
            "qrels": qrels,
        },
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return f"sha256:{hashlib.sha256(payload).hexdigest()}"


def document_matches_filters(document: Document, filters: Mapping[str, Any]) -> bool:
    if "workspace" in filters and document.workspace != filters["workspace"]:
        return False
    if "path_prefix" in filters:
        prefix = filters["path_prefix"].rstrip("/") or "/"
        if prefix != "/" and not (
            document.path == prefix or document.path.startswith(f"{prefix}/")
        ):
            return False
    if "source_kind" in filters and document.source_kind != filters["source_kind"]:
        return False
    requested_metadata = filters.get("metadata", {})
    return all(
        document.metadata.get(key) == value for key, value in requested_metadata.items()
    )


def load_dataset(root: str | Path) -> Dataset:
    dataset_root = Path(root)
    metadata = _read_json(dataset_root / "dataset.json")
    corpus_records = _read_jsonl(dataset_root / "corpus.jsonl")
    query_records = _read_jsonl(dataset_root / "queries.jsonl")
    qrels_record = _read_json(dataset_root / "qrels.json")

    if not isinstance(metadata, dict):
        raise BenchmarkError("dataset.json must be an object")
    for field in ("schema_version", "version", "provenance", "license"):
        _required_string(metadata, field, "dataset")
    if metadata["provenance"] != "hand-authored synthetic data":
        raise BenchmarkError(
            "dataset.provenance must be 'hand-authored synthetic data'"
        )

    documents: list[Document] = []
    document_ids: set[str] = set()
    for index, record in enumerate(corpus_records, start=1):
        context = f"corpus[{index}]"
        doc_id = _required_string(record, "id", context)
        if doc_id in document_ids:
            raise BenchmarkError(f"duplicate document id: {doc_id}")
        document_ids.add(doc_id)
        language = _required_string(record, "language", context)
        source_kind = _required_string(record, "source_kind", context)
        if language not in REQUIRED_LANGUAGES:
            raise BenchmarkError(f"{context}.language is unsupported: {language}")
        if source_kind not in REQUIRED_SOURCE_KINDS:
            raise BenchmarkError(f"{context}.source_kind is unsupported: {source_kind}")
        if record.get("provenance") != "synthetic":
            raise BenchmarkError(f"{context}.provenance must be 'synthetic'")
        raw_metadata = record.get("metadata", {})
        if not isinstance(raw_metadata, dict):
            raise BenchmarkError(f"{context}.metadata must be an object")
        workspace = _required_string(record, "workspace", context)
        path = _required_string(record, "path", context)
        citation = _required_string(record, "citation", context)
        if not _is_clean_absolute_path(path, allow_trailing_slash=False):
            raise BenchmarkError(f"{context}.path must be an absolute clean path")
        if not citation.startswith("mem://"):
            raise BenchmarkError(f"{context}.citation must start with 'mem://'")
        documents.append(
            Document(
                id=doc_id,
                language=language,
                source_kind=source_kind,
                workspace=workspace,
                path=path,
                citation=citation,
                text=_required_string(record, "text", context),
                metadata=raw_metadata,
            )
        )

    queries: list[Query] = []
    query_ids: set[str] = set()
    for index, record in enumerate(query_records, start=1):
        context = f"queries[{index}]"
        query_id = _required_string(record, "id", context)
        if query_id in query_ids:
            raise BenchmarkError(f"duplicate query id: {query_id}")
        query_ids.add(query_id)
        language = _required_string(record, "language", context)
        scenario_slice = _required_string(record, "slice", context)
        expected_source_kind = _required_string(
            record,
            "expected_source_kind",
            context,
        )
        if language not in REQUIRED_LANGUAGES:
            raise BenchmarkError(f"{context}.language is unsupported: {language}")
        if scenario_slice not in REQUIRED_SLICES:
            raise BenchmarkError(f"{context}.slice is unsupported: {scenario_slice}")
        if expected_source_kind not in REQUIRED_SOURCE_KINDS:
            raise BenchmarkError(
                f"{context}.expected_source_kind is unsupported: {expected_source_kind}"
            )
        filters = record.get("filters")
        if not isinstance(filters, dict):
            raise BenchmarkError(f"{context}.filters must be an object")
        unknown_filters = set(filters).difference(ALLOWED_FILTERS)
        if unknown_filters:
            raise BenchmarkError(
                f"{context}.filters has unsupported keys: {sorted(unknown_filters)}"
            )
        if "metadata" in filters and not isinstance(filters["metadata"], dict):
            raise BenchmarkError(f"{context}.filters.metadata must be an object")
        for field in ("workspace", "path_prefix", "source_kind"):
            if field in filters and not isinstance(filters[field], str):
                raise BenchmarkError(f"{context}.filters.{field} must be a string")
        if "path_prefix" in filters and not _is_clean_absolute_path(
            filters["path_prefix"],
            allow_trailing_slash=True,
        ):
            raise BenchmarkError(
                f"{context}.filters.path_prefix must be an absolute clean path"
            )
        queries.append(
            Query(
                id=query_id,
                text=_required_string(record, "text", context),
                language=language,
                slice=scenario_slice,
                filters=filters,
                expected_source_kind=expected_source_kind,
            )
        )

    if not isinstance(qrels_record, dict):
        raise BenchmarkError("qrels.json must be an object")
    if set(qrels_record) != query_ids:
        missing = sorted(query_ids.difference(qrels_record))
        extra = sorted(set(qrels_record).difference(query_ids))
        raise BenchmarkError(f"qrels/query mismatch: missing={missing}, extra={extra}")

    documents_by_id = {document.id: document for document in documents}
    qrels: dict[str, dict[str, float]] = {}
    queries_by_id = {query.id: query for query in queries}
    for query_id, raw_judgments in qrels_record.items():
        if not isinstance(raw_judgments, dict):
            raise BenchmarkError(f"qrels.{query_id} must be an object")
        judgments: dict[str, float] = {}
        for doc_id, raw_grade in raw_judgments.items():
            if doc_id not in documents_by_id:
                raise BenchmarkError(
                    f"qrels.{query_id} references unknown document: {doc_id}"
                )
            if (
                not isinstance(raw_grade, (int, float))
                or isinstance(raw_grade, bool)
                or not math.isfinite(float(raw_grade))
                or not 0 <= float(raw_grade) <= 3
            ):
                raise BenchmarkError(
                    f"qrels.{query_id}.{doc_id} must be a finite number from 0 to 3"
                )
            judgments[doc_id] = float(raw_grade)
        positive = [doc_id for doc_id, grade in judgments.items() if grade > 0]
        if not positive:
            raise BenchmarkError(f"qrels.{query_id} needs a positive judgment")
        query = queries_by_id[query_id]
        forbidden_qrels = [
            doc_id
            for doc_id in positive
            if not document_matches_filters(documents_by_id[doc_id], query.filters)
        ]
        if forbidden_qrels:
            raise BenchmarkError(
                f"qrels.{query_id} contains filter-ineligible documents: "
                f"{sorted(forbidden_qrels)}"
            )
        qrels[query_id] = judgments

    required_coverage = metadata.get(
        "required_coverage",
        {
            "slices": sorted(REQUIRED_SLICES),
            "languages": sorted(REQUIRED_LANGUAGES),
            "source_kinds": sorted(REQUIRED_SOURCE_KINDS),
        },
    )
    if not isinstance(required_coverage, dict):
        raise BenchmarkError("dataset.required_coverage must be an object")
    coverage_values: dict[str, set[str]] = {}
    allowed_coverage = {
        "slices": REQUIRED_SLICES,
        "languages": REQUIRED_LANGUAGES,
        "source_kinds": REQUIRED_SOURCE_KINDS,
    }
    for field, allowed in allowed_coverage.items():
        raw_values = required_coverage.get(field)
        if (
            not isinstance(raw_values, list)
            or not raw_values
            or any(not isinstance(value, str) for value in raw_values)
        ):
            raise BenchmarkError(
                f"dataset.required_coverage.{field} must be a non-empty string array"
            )
        values = set(raw_values)
        unknown = values.difference(allowed)
        if unknown:
            raise BenchmarkError(
                f"dataset.required_coverage.{field} has unsupported values: "
                f"{sorted(unknown)}"
            )
        coverage_values[field] = values
    missing_slices = coverage_values["slices"].difference(
        query.slice for query in queries
    )
    missing_languages = coverage_values["languages"].difference(
        query.language for query in queries
    )
    missing_sources = coverage_values["source_kinds"].difference(
        query.expected_source_kind for query in queries
    )
    if missing_slices or missing_languages or missing_sources:
        raise BenchmarkError(
            "dataset coverage incomplete: "
            f"slices={sorted(missing_slices)}, "
            f"languages={sorted(missing_languages)}, "
            f"source_kinds={sorted(missing_sources)}"
        )

    return Dataset(
        root=dataset_root,
        metadata=metadata,
        documents=tuple(documents),
        queries=tuple(queries),
        qrels=qrels,
        checksum=_canonical_checksum(
            metadata,
            corpus_records,
            query_records,
            qrels_record,
        ),
    )
