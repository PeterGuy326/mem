from __future__ import annotations

from collections import defaultdict
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
from typing import Any, Iterable, Mapping

from .adapters import lexical_rankings, load_external_rankings
from .dataset import Dataset, document_matches_filters, load_dataset
from .errors import BenchmarkError
from .metrics import (
    METRIC_NAMES,
    aggregate_query_metrics,
    dedupe_results,
    evaluate_query,
)


ARTIFACT_SCHEMA = "mem.recall-benchmark.v1"
DISCLAIMER = "not production recall"


def _ensure_supported_python() -> None:
    if sys.version_info < (3, 11):
        raise BenchmarkError(
            "the recall benchmark requires Python 3.11 or newer; "
            f"found {platform.python_version()}"
        )


def _repository_state(repository_root: Path) -> dict[str, Any]:
    try:
        commit = subprocess.run(
            ["git", "rev-parse", "HEAD"],
            cwd=repository_root,
            check=True,
            capture_output=True,
            text=True,
            timeout=5,
        ).stdout.strip()
        dirty = bool(
            subprocess.run(
                ["git", "status", "--porcelain"],
                cwd=repository_root,
                check=True,
                capture_output=True,
                text=True,
                timeout=5,
            ).stdout.strip()
        )
    except (OSError, subprocess.SubprocessError):
        commit = "unavailable"
        dirty = None
    return {"commit": commit, "dirty": dirty}


def _runtime_summary() -> dict[str, Any]:
    return {
        "python": platform.python_version(),
        "implementation": platform.python_implementation(),
        "platform": platform.system(),
        "machine": platform.machine(),
        "cpu_count": os.cpu_count(),
    }


def _aggregate_groups(
    query_metrics: Iterable[Mapping[str, Any]],
    field: str,
) -> dict[str, dict[str, Any]]:
    groups: defaultdict[str, list[Mapping[str, Any]]] = defaultdict(list)
    for row in query_metrics:
        groups[str(row[field])].append(row)
    return {key: aggregate_query_metrics(groups[key]) for key in sorted(groups)}


def _evaluate(
    dataset: Dataset,
    rankings: Mapping[str, Any],
) -> tuple[list[dict[str, Any]], list[dict[str, str]]]:
    documents = dataset.documents_by_id
    rankings_by_query = {str(row["query_id"]): row for row in rankings["queries"]}
    query_rows: list[dict[str, Any]] = []
    failures: list[dict[str, str]] = []
    for query in dataset.queries:
        ranking = rankings_by_query[query.id]
        results = dedupe_results(ranking["results"])
        leakage_doc_ids: list[str] = []
        for result in results:
            doc_id = str(result.get("doc_id", ""))
            document = documents.get(doc_id)
            if document is None or not document_matches_filters(
                document,
                query.filters,
            ):
                leakage_doc_ids.append(doc_id)
        metrics = evaluate_query(
            results=results,
            qrels=dataset.qrels[query.id],
            canonical_citations={
                doc_id: document.citation for doc_id, document in documents.items()
            },
            source_kinds={
                doc_id: document.source_kind for doc_id, document in documents.items()
            },
            expected_source_kind=query.expected_source_kind,
        )
        row = {
            "query_id": query.id,
            "language": query.language,
            "slice": query.slice,
            "source_kind": query.expected_source_kind,
            "status": ranking["status"],
            "latency_ms": float(ranking["latency_ms"]),
            "returned_count": len(results),
            "leakage_count": len(leakage_doc_ids),
            "leakage_doc_ids": leakage_doc_ids,
            "results": results,
            **metrics,
        }
        query_rows.append(row)
        if ranking["status"] != "ok":
            failures.append(
                {
                    "query_id": query.id,
                    "status": ranking["status"],
                    "error_code": ranking.get("error_code", "unspecified"),
                }
            )
    return query_rows, failures


def run_benchmark(
    *,
    dataset_dir: str | Path,
    rankings_path: str | Path | None = None,
    repository_root: str | Path | None = None,
    generated_at: str | None = None,
) -> dict[str, Any]:
    _ensure_supported_python()
    dataset = load_dataset(dataset_dir)
    rankings = (
        load_external_rankings(rankings_path, dataset)
        if rankings_path is not None
        else lexical_rankings(dataset)
    )
    query_rows, failures = _evaluate(dataset, rankings)
    root = (
        Path(repository_root)
        if repository_root is not None
        else Path(__file__).resolve().parents[2]
    )
    timestamp = generated_at or datetime.now(timezone.utc).isoformat()
    return {
        "schema_version": ARTIFACT_SCHEMA,
        "generated_at": timestamp,
        "engine": rankings["engine"],
        "disclaimer": DISCLAIMER,
        "repository": _repository_state(root),
        "dataset": {
            "schema_version": dataset.metadata["schema_version"],
            "version": dataset.metadata["version"],
            "checksum": dataset.checksum,
            "provenance": dataset.metadata["provenance"],
            "license": dataset.metadata["license"],
            "document_count": len(dataset.documents),
            "query_count": len(dataset.queries),
        },
        "configuration": rankings["configuration"],
        "runtime": {
            **_runtime_summary(),
            "hardware": rankings["hardware"],
        },
        "metric_rules": {
            "ranking": (
                "external order is authoritative; lexical ties use document id "
                "ascending"
            ),
            "dedupe": "first occurrence of a document id wins before scoring",
            "recall_denominator": "all qrels with relevance > 0 for that query",
            "macro_denominator": (
                "all dataset queries, equally weighted; error and partial queries "
                "remain in the denominator"
            ),
            "citation_denominator": (
                "all deduplicated returned results through rank 10; empty is zero"
            ),
            "source_denominator": (
                "one per query; the first relevant result must have the expected "
                "source kind, and no relevant result scores zero"
            ),
            "latency_percentile": "nearest-rank over every query status",
            "leakage": ("unknown document ids or documents forbidden by query filters"),
        },
        "metrics": {
            "overall": aggregate_query_metrics(query_rows),
            "by_slice": _aggregate_groups(query_rows, "slice"),
            "by_language": _aggregate_groups(query_rows, "language"),
            "by_source_kind": _aggregate_groups(query_rows, "source_kind"),
        },
        "failures": failures,
        "queries": query_rows,
    }


def write_json(path: str | Path, payload: Mapping[str, Any]) -> None:
    output = Path(path)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(
        json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )


def load_artifact(path: str | Path) -> dict[str, Any]:
    try:
        artifact = json.loads(Path(path).read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise BenchmarkError(f"artifact not found: {path}") from exc
    except json.JSONDecodeError as exc:
        raise BenchmarkError(
            f"invalid artifact JSON at line {exc.lineno}: {exc.msg}"
        ) from exc
    if (
        not isinstance(artifact, dict)
        or artifact.get("schema_version") != ARTIFACT_SCHEMA
    ):
        raise BenchmarkError(f"artifact schema must be {ARTIFACT_SCHEMA!r}")
    return artifact


def compare_artifacts(
    baseline: Mapping[str, Any],
    candidate: Mapping[str, Any],
) -> dict[str, Any]:
    baseline_dataset = baseline.get("dataset", {})
    candidate_dataset = candidate.get("dataset", {})
    if baseline_dataset.get("checksum") != candidate_dataset.get("checksum"):
        raise BenchmarkError(
            "cannot compare artifacts from different dataset checksums"
        )
    scalar_metrics = [
        *METRIC_NAMES,
        "error_rate",
        "partial_rate",
        "leakage_count",
        "leakage_rate",
    ]

    def metric_comparison(
        baseline_metrics: Mapping[str, Any],
        candidate_metrics: Mapping[str, Any],
    ) -> dict[str, Any]:
        absolute = {
            "baseline": {name: baseline_metrics[name] for name in scalar_metrics},
            "candidate": {name: candidate_metrics[name] for name in scalar_metrics},
        }
        delta = {
            name: float(candidate_metrics[name]) - float(baseline_metrics[name])
            for name in scalar_metrics
        }
        for percentile in ("p50", "p95"):
            key = f"latency_ms_{percentile}"
            absolute["baseline"][key] = baseline_metrics["latency_ms"][percentile]
            absolute["candidate"][key] = candidate_metrics["latency_ms"][percentile]
            delta[key] = float(candidate_metrics["latency_ms"][percentile]) - float(
                baseline_metrics["latency_ms"][percentile]
            )
        return {"absolute": absolute, "delta": delta}

    overall = metric_comparison(
        baseline["metrics"]["overall"],
        candidate["metrics"]["overall"],
    )
    breakdowns: dict[str, dict[str, Any]] = {}
    for breakdown in ("by_slice", "by_language", "by_source_kind"):
        baseline_groups = baseline["metrics"][breakdown]
        candidate_groups = candidate["metrics"][breakdown]
        if set(baseline_groups) != set(candidate_groups):
            raise BenchmarkError(
                f"cannot compare artifacts with different {breakdown} groups"
            )
        breakdowns[breakdown] = {
            group: metric_comparison(
                baseline_groups[group],
                candidate_groups[group],
            )
            for group in sorted(candidate_groups)
        }
    return {
        "schema_version": "mem.recall-comparison.v1",
        "dataset": {
            "version": candidate_dataset.get("version"),
            "checksum": candidate_dataset.get("checksum"),
        },
        "engines": {
            "baseline": baseline.get("engine"),
            "candidate": candidate.get("engine"),
        },
        "absolute": overall["absolute"],
        "delta": overall["delta"],
        "breakdowns": breakdowns,
        "policy": "informational-only; no quality threshold is enforced",
    }


def human_summary(artifact: Mapping[str, Any]) -> str:
    metrics = artifact["metrics"]["overall"]
    return "\n".join(
        [
            (f"Recall benchmark ({artifact['engine']}; {artifact['disclaimer']})"),
            (
                "quality: "
                f"R@1={metrics['recall_at_1']:.3f} "
                f"R@5={metrics['recall_at_5']:.3f} "
                f"R@10={metrics['recall_at_10']:.3f} "
                f"MRR={metrics['mrr']:.3f} "
                f"nDCG@10={metrics['ndcg_at_10']:.3f}"
            ),
            (
                "correctness: "
                f"citation={metrics['citation_accuracy']:.3f} "
                f"source={metrics['source_accuracy']:.3f} "
                f"leaks={metrics['leakage_count']}"
            ),
            (
                "reliability: "
                f"error={metrics['error_rate']:.3f} "
                f"partial={metrics['partial_rate']:.3f} "
                f"p50={metrics['latency_ms']['p50']:.3f}ms "
                f"p95={metrics['latency_ms']['p95']:.3f}ms"
            ),
        ]
    )


def comparison_summary(comparison: Mapping[str, Any]) -> str:
    candidate = comparison["absolute"]["candidate"]
    delta = comparison["delta"]
    return "\n".join(
        [
            (
                f"Comparison: {comparison['engines']['candidate']} vs "
                f"{comparison['engines']['baseline']} "
                f"({comparison['policy']})"
            ),
            (
                "absolute/delta: "
                f"R@1={candidate['recall_at_1']:.3f}"
                f" ({delta['recall_at_1']:+.3f}), "
                f"R@5={candidate['recall_at_5']:.3f}"
                f" ({delta['recall_at_5']:+.3f}), "
                f"R@10={candidate['recall_at_10']:.3f}"
                f" ({delta['recall_at_10']:+.3f}), "
                f"MRR={candidate['mrr']:.3f} ({delta['mrr']:+.3f}), "
                f"nDCG@10={candidate['ndcg_at_10']:.3f}"
                f" ({delta['ndcg_at_10']:+.3f})"
            ),
            (
                "safety/reliability: "
                f"leaks={candidate['leakage_count']}"
                f" ({delta['leakage_count']:+.0f}), "
                f"error={candidate['error_rate']:.3f}"
                f" ({delta['error_rate']:+.3f}), "
                f"partial={candidate['partial_rate']:.3f}"
                f" ({delta['partial_rate']:+.3f})"
            ),
        ]
    )


def equivalent_ignoring_timestamps(
    first: Mapping[str, Any],
    second: Mapping[str, Any],
) -> bool:
    first_copy = json.loads(json.dumps(first))
    second_copy = json.loads(json.dumps(second))
    first_copy.pop("generated_at", None)
    second_copy.pop("generated_at", None)
    return first_copy == second_copy


__all__ = [
    "BenchmarkError",
    "compare_artifacts",
    "comparison_summary",
    "equivalent_ignoring_timestamps",
    "human_summary",
    "load_artifact",
    "run_benchmark",
    "write_json",
]
