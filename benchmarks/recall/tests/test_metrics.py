from __future__ import annotations

import math
import unittest

from benchmarks.recall.metrics import (
    aggregate_query_metrics,
    dedupe_results,
    evaluate_query,
)


class MetricsTest(unittest.TestCase):
    def test_deduplicates_by_document_id_and_keeps_first_occurrence(self) -> None:
        results = [
            {"doc_id": "doc-b", "citation": "mem://doc-b/first"},
            {"doc_id": "doc-a", "citation": "mem://doc-a"},
            {"doc_id": "doc-b", "citation": "mem://doc-b/second"},
        ]

        self.assertEqual(
            dedupe_results(results),
            [
                {"doc_id": "doc-b", "citation": "mem://doc-b/first"},
                {"doc_id": "doc-a", "citation": "mem://doc-a"},
            ],
        )

    def test_query_metrics_use_all_positive_qrels_as_recall_denominator(self) -> None:
        metrics = evaluate_query(
            results=[
                {"doc_id": "irrelevant", "citation": "mem://irrelevant"},
                {"doc_id": "relevant-high", "citation": "mem://relevant-high"},
                {"doc_id": "relevant-low", "citation": "mem://relevant-low"},
            ],
            qrels={"relevant-high": 3, "relevant-low": 1, "ignored": 0},
            canonical_citations={
                "irrelevant": "mem://irrelevant",
                "relevant-high": "mem://relevant-high",
                "relevant-low": "mem://relevant-low",
            },
            source_kinds={
                "irrelevant": "text",
                "relevant-high": "structured",
                "relevant-low": "structured",
            },
            expected_source_kind="structured",
        )

        self.assertEqual(metrics["recall_at_1"], 0.0)
        self.assertEqual(metrics["recall_at_5"], 1.0)
        self.assertEqual(metrics["recall_at_10"], 1.0)
        self.assertEqual(metrics["mrr"], 0.5)
        expected_dcg = 7 / math.log2(3) + 1 / math.log2(4)
        expected_idcg = 7 / math.log2(2) + 1 / math.log2(3)
        self.assertAlmostEqual(metrics["ndcg_at_10"], expected_dcg / expected_idcg)
        self.assertEqual(metrics["citation_accuracy"], 1.0)
        self.assertEqual(metrics["source_accuracy"], 1.0)

    def test_missing_or_wrong_citations_count_against_returned_result_denominator(
        self,
    ) -> None:
        metrics = evaluate_query(
            results=[
                {"doc_id": "doc-a", "citation": "wrong"},
                {"doc_id": "doc-b"},
                {"doc_id": "doc-c", "citation": "mem://doc-c"},
            ],
            qrels={"doc-a": 1},
            canonical_citations={
                "doc-a": "mem://doc-a",
                "doc-b": "mem://doc-b",
                "doc-c": "mem://doc-c",
            },
            source_kinds={
                "doc-a": "text",
                "doc-b": "text",
                "doc-c": "text",
            },
            expected_source_kind="text",
        )

        self.assertAlmostEqual(metrics["citation_accuracy"], 1 / 3)

    def test_unknown_result_without_citation_is_not_counted_as_correct(self) -> None:
        metrics = evaluate_query(
            results=[{"doc_id": "unknown"}],
            qrels={"relevant": 1},
            canonical_citations={"relevant": "mem://relevant"},
            source_kinds={"relevant": "text"},
            expected_source_kind="text",
        )

        self.assertEqual(metrics["citation_accuracy"], 0.0)

    def test_macro_aggregation_weights_queries_equally_and_includes_errors(
        self,
    ) -> None:
        aggregated = aggregate_query_metrics(
            [
                {
                    "query_id": "ok",
                    "status": "ok",
                    "latency_ms": 10.0,
                    "recall_at_1": 1.0,
                    "recall_at_5": 1.0,
                    "recall_at_10": 1.0,
                    "mrr": 1.0,
                    "ndcg_at_10": 1.0,
                    "citation_accuracy": 1.0,
                    "source_accuracy": 1.0,
                    "leakage_count": 0,
                },
                {
                    "query_id": "error",
                    "status": "error",
                    "latency_ms": 30.0,
                    "recall_at_1": 0.0,
                    "recall_at_5": 0.0,
                    "recall_at_10": 0.0,
                    "mrr": 0.0,
                    "ndcg_at_10": 0.0,
                    "citation_accuracy": 0.0,
                    "source_accuracy": 0.0,
                    "leakage_count": 0,
                },
                {
                    "query_id": "partial",
                    "status": "partial",
                    "latency_ms": 20.0,
                    "recall_at_1": 0.0,
                    "recall_at_5": 0.0,
                    "recall_at_10": 0.0,
                    "mrr": 0.0,
                    "ndcg_at_10": 0.0,
                    "citation_accuracy": 0.0,
                    "source_accuracy": 0.0,
                    "leakage_count": 0,
                },
            ]
        )

        self.assertEqual(aggregated["query_count"], 3)
        self.assertEqual(aggregated["recall_at_1"], 1 / 3)
        self.assertEqual(aggregated["mrr"], 1 / 3)
        self.assertEqual(aggregated["error_rate"], 1 / 3)
        self.assertEqual(aggregated["partial_rate"], 1 / 3)
        self.assertEqual(aggregated["latency_ms"]["p50"], 20.0)
        self.assertEqual(aggregated["latency_ms"]["p95"], 30.0)


if __name__ == "__main__":
    unittest.main()
