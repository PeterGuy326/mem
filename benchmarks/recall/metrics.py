from __future__ import annotations

import math
from typing import Any, Iterable, Mapping, Sequence


METRIC_NAMES = (
    "recall_at_1",
    "recall_at_5",
    "recall_at_10",
    "mrr",
    "ndcg_at_10",
    "citation_accuracy",
    "source_accuracy",
)


def dedupe_results(results: Sequence[Mapping[str, Any]]) -> list[dict[str, Any]]:
    """Keep the first occurrence of each document without reordering results."""
    seen: set[str] = set()
    deduped: list[dict[str, Any]] = []
    for result in results:
        doc_id = str(result.get("doc_id", ""))
        if doc_id in seen:
            continue
        seen.add(doc_id)
        deduped.append(dict(result))
    return deduped


def evaluate_query(
    *,
    results: Sequence[Mapping[str, Any]],
    qrels: Mapping[str, float],
    canonical_citations: Mapping[str, str],
    source_kinds: Mapping[str, str],
    expected_source_kind: str,
) -> dict[str, float]:
    """Calculate metrics for one query using positive qrels as relevant docs."""
    ranked = dedupe_results(results)
    positive_qrels = {
        doc_id: float(relevance)
        for doc_id, relevance in qrels.items()
        if float(relevance) > 0
    }
    relevant = set(positive_qrels)
    if not relevant:
        raise ValueError("a query must have at least one positive qrel")

    ranked_ids = [str(result.get("doc_id", "")) for result in ranked]

    def recall_at(k: int) -> float:
        retrieved = relevant.intersection(ranked_ids[:k])
        return len(retrieved) / len(relevant)

    reciprocal_rank = 0.0
    first_relevant_id: str | None = None
    for rank, doc_id in enumerate(ranked_ids, start=1):
        if doc_id in relevant:
            reciprocal_rank = 1.0 / rank
            first_relevant_id = doc_id
            break

    dcg = 0.0
    for rank, doc_id in enumerate(ranked_ids[:10], start=1):
        grade = positive_qrels.get(doc_id, 0.0)
        dcg += (2**grade - 1) / math.log2(rank + 1)
    ideal_grades = sorted(positive_qrels.values(), reverse=True)[:10]
    idcg = sum(
        (2**grade - 1) / math.log2(rank + 1)
        for rank, grade in enumerate(ideal_grades, start=1)
    )

    citation_results = ranked[:10]
    correct_citations = sum(
        1
        for result in citation_results
        if str(result.get("doc_id", "")) in canonical_citations
        and canonical_citations[str(result["doc_id"])] == result.get("citation")
    )
    citation_accuracy = (
        correct_citations / len(citation_results) if citation_results else 0.0
    )
    source_accuracy = float(
        first_relevant_id is not None
        and source_kinds.get(first_relevant_id) == expected_source_kind
    )

    return {
        "recall_at_1": recall_at(1),
        "recall_at_5": recall_at(5),
        "recall_at_10": recall_at(10),
        "mrr": reciprocal_rank,
        "ndcg_at_10": dcg / idcg if idcg else 0.0,
        "citation_accuracy": citation_accuracy,
        "source_accuracy": source_accuracy,
    }


def _nearest_rank(values: Sequence[float], percentile: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    index = max(0, math.ceil(percentile * len(ordered)) - 1)
    return ordered[index]


def aggregate_query_metrics(
    query_metrics: Iterable[Mapping[str, Any]],
) -> dict[str, Any]:
    rows = list(query_metrics)
    if not rows:
        return {
            "query_count": 0,
            **{name: 0.0 for name in METRIC_NAMES},
            "error_rate": 0.0,
            "partial_rate": 0.0,
            "latency_ms": {"p50": 0.0, "p95": 0.0},
            "leakage_count": 0,
            "leakage_rate": 0.0,
        }

    query_count = len(rows)
    aggregated: dict[str, Any] = {
        "query_count": query_count,
        **{
            name: sum(float(row[name]) for row in rows) / query_count
            for name in METRIC_NAMES
        },
        "error_rate": sum(row["status"] == "error" for row in rows) / query_count,
        "partial_rate": (sum(row["status"] == "partial" for row in rows) / query_count),
        "latency_ms": {
            "p50": _nearest_rank(
                [float(row["latency_ms"]) for row in rows],
                0.50,
            ),
            "p95": _nearest_rank(
                [float(row["latency_ms"]) for row in rows],
                0.95,
            ),
        },
        "leakage_count": sum(int(row["leakage_count"]) for row in rows),
    }
    returned_count = sum(int(row.get("returned_count", 0)) for row in rows)
    aggregated["leakage_rate"] = (
        aggregated["leakage_count"] / returned_count if returned_count else 0.0
    )
    return aggregated
