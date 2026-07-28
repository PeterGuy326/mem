from __future__ import annotations

import json
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path
import tempfile
import unittest

from benchmarks.recall.runner import BenchmarkError, compare_artifacts, run_benchmark
from benchmarks.recall.__main__ import main
from benchmarks.recall.runner import equivalent_ignoring_timestamps


RECALL_ROOT = Path(__file__).resolve().parents[1]


class RunnerTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        self.dataset = self.root / "dataset"
        self.dataset.mkdir()
        (self.dataset / "dataset.json").write_text(
            json.dumps(
                {
                    "schema_version": "mem.recall-dataset.v1",
                    "version": "unit-test-v1",
                    "provenance": "hand-authored synthetic data",
                    "license": "CC0-1.0",
                    "required_coverage": {
                        "slices": ["filters"],
                        "languages": ["en"],
                        "source_kinds": ["text"],
                    },
                }
            ),
            encoding="utf-8",
        )
        (self.dataset / "corpus.jsonl").write_text(
            "\n".join(
                [
                    json.dumps(
                        {
                            "id": "allowed",
                            "language": "en",
                            "source_kind": "text",
                            "workspace": "alpha",
                            "path": "/notes/allowed.md",
                            "citation": "mem://files/allowed",
                            "text": "saturn ring observation",
                            "provenance": "synthetic",
                        }
                    ),
                    json.dumps(
                        {
                            "id": "forbidden",
                            "language": "en",
                            "source_kind": "text",
                            "workspace": "beta",
                            "path": "/notes/forbidden.md",
                            "citation": "mem://files/forbidden",
                            "text": "saturn ring observation forbidden",
                            "provenance": "synthetic",
                        }
                    ),
                ]
            )
            + "\n",
            encoding="utf-8",
        )
        (self.dataset / "queries.jsonl").write_text(
            json.dumps(
                {
                    "id": "q1",
                    "text": "saturn ring",
                    "language": "en",
                    "slice": "filters",
                    "filters": {"workspace": "alpha", "path_prefix": "/notes/"},
                    "expected_source_kind": "text",
                }
            )
            + "\n",
            encoding="utf-8",
        )
        (self.dataset / "qrels.json").write_text(
            json.dumps({"q1": {"allowed": 2}}),
            encoding="utf-8",
        )

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    @staticmethod
    def _external_payload(
        *,
        status: str = "ok",
        results: list[dict[str, object]] | None = None,
    ) -> dict[str, object]:
        return {
            "schema_version": "mem.recall-rankings.v1",
            "engine": "unit-candidate",
            "configuration": {
                "mode": "vector",
                "provider": "unit-provider",
                "model": "unit-model",
                "dimension": 3,
                "index": {"kind": "unit-index"},
                "search": {"top_k": 10},
            },
            "hardware": {"class": "unit-test"},
            "queries": [
                {
                    "query_id": "q1",
                    "status": status,
                    "latency_ms": 5,
                    "results": results or [],
                    **({"error_code": "fixture_error"} if status != "ok" else {}),
                }
            ],
        }

    def test_lexical_adapter_applies_filters_and_has_no_leakage(self) -> None:
        artifact = run_benchmark(dataset_dir=self.dataset)

        self.assertEqual(artifact["engine"], "lexical-reference")
        self.assertEqual(artifact["disclaimer"], "not production recall")
        self.assertEqual(artifact["metrics"]["overall"]["leakage_count"], 0)
        self.assertEqual(
            artifact["queries"][0]["results"][0]["doc_id"],
            "allowed",
        )

    def test_equal_lexical_scores_are_tied_by_document_id(self) -> None:
        corpus_path = self.dataset / "corpus.jsonl"
        corpus_path.write_text(
            corpus_path.read_text(encoding="utf-8")
            + json.dumps(
                {
                    "id": "aaa",
                    "language": "en",
                    "source_kind": "text",
                    "workspace": "alpha",
                    "path": "/notes/aaa.md",
                    "citation": "mem://files/aaa",
                    "text": "saturn ring observation",
                    "provenance": "synthetic",
                }
            )
            + "\n",
            encoding="utf-8",
        )

        artifact = run_benchmark(dataset_dir=self.dataset)

        self.assertEqual(
            [result["doc_id"] for result in artifact["queries"][0]["results"][:2]],
            ["aaa", "allowed"],
        )

    def test_external_adapter_requires_status_and_latency_for_every_query(self) -> None:
        rankings = self.root / "rankings.json"
        payload = self._external_payload()
        del payload["queries"][0]["status"]  # type: ignore[index]
        rankings.write_text(
            json.dumps(payload),
            encoding="utf-8",
        )

        with self.assertRaisesRegex(BenchmarkError, "status"):
            run_benchmark(dataset_dir=self.dataset, rankings_path=rankings)

    def test_external_adapter_never_infers_model_configuration(self) -> None:
        rankings = self.root / "rankings.json"
        payload = self._external_payload()
        del payload["configuration"]["dimension"]  # type: ignore[index]
        rankings.write_text(json.dumps(payload), encoding="utf-8")

        with self.assertRaisesRegex(BenchmarkError, "missing fields.*dimension"):
            run_benchmark(dataset_dir=self.dataset, rankings_path=rankings)

    def test_external_lexical_mode_explicitly_uses_null_model_fields(self) -> None:
        rankings = self.root / "rankings.json"
        payload = self._external_payload(
            results=[
                {
                    "doc_id": "allowed",
                    "citation": "mem://files/allowed",
                }
            ]
        )
        payload["configuration"] = {
            "mode": "lexical",
            "provider": None,
            "model": None,
            "dimension": None,
            "index": {"kind": "external-lexical"},
            "search": {"top_k": 10},
        }
        rankings.write_text(json.dumps(payload), encoding="utf-8")

        artifact = run_benchmark(dataset_dir=self.dataset, rankings_path=rankings)

        self.assertEqual(artifact["configuration"]["mode"], "lexical")
        self.assertIsNone(artifact["configuration"]["dimension"])

    def test_external_metadata_rejects_credential_fields(self) -> None:
        rankings = self.root / "rankings.json"
        payload = self._external_payload()
        payload["configuration"]["index"]["api_key"] = "must-not-be-copied"  # type: ignore[index]
        rankings.write_text(json.dumps(payload), encoding="utf-8")

        with self.assertRaisesRegex(BenchmarkError, "sensitive key: api_key"):
            run_benchmark(dataset_dir=self.dataset, rankings_path=rankings)

    def test_out_of_scope_external_result_is_reported_as_leakage(self) -> None:
        rankings = self.root / "rankings.json"
        rankings.write_text(
            json.dumps(
                self._external_payload(
                    results=[
                        {
                            "doc_id": "forbidden",
                            "citation": "mem://files/forbidden",
                        },
                        {
                            "doc_id": "allowed",
                            "citation": "mem://files/allowed",
                        },
                    ]
                )
            ),
            encoding="utf-8",
        )

        artifact = run_benchmark(
            dataset_dir=self.dataset,
            rankings_path=rankings,
        )

        self.assertEqual(artifact["metrics"]["overall"]["leakage_count"], 1)
        self.assertEqual(artifact["queries"][0]["leakage_doc_ids"], ["forbidden"])

    def test_duplicate_results_are_scored_once_and_keep_first_citation(self) -> None:
        rankings = self.root / "rankings.json"
        rankings.write_text(
            json.dumps(
                self._external_payload(
                    results=[
                        {"doc_id": "allowed", "citation": "first"},
                        {
                            "doc_id": "allowed",
                            "citation": "mem://files/allowed",
                        },
                    ]
                )
            ),
            encoding="utf-8",
        )

        artifact = run_benchmark(dataset_dir=self.dataset, rankings_path=rankings)

        query = artifact["queries"][0]
        self.assertEqual(query["returned_count"], 1)
        self.assertEqual(query["results"][0]["citation"], "first")
        self.assertEqual(query["citation_accuracy"], 0.0)

    def test_error_status_is_explicit_missing_result_and_scores_zero(self) -> None:
        rankings = self.root / "rankings.json"
        rankings.write_text(
            json.dumps(self._external_payload(status="error")),
            encoding="utf-8",
        )

        artifact = run_benchmark(dataset_dir=self.dataset, rankings_path=rankings)

        self.assertEqual(artifact["metrics"]["overall"]["error_rate"], 1.0)
        self.assertEqual(artifact["metrics"]["overall"]["recall_at_10"], 0.0)
        self.assertEqual(
            artifact["failures"],
            [
                {
                    "query_id": "q1",
                    "status": "error",
                    "error_code": "fixture_error",
                }
            ],
        )

    def test_omitted_query_is_rejected_instead_of_silently_scored_missing(self) -> None:
        (self.dataset / "queries.jsonl").write_text(
            (self.dataset / "queries.jsonl").read_text(encoding="utf-8")
            + json.dumps(
                {
                    "id": "q2",
                    "text": "observation",
                    "language": "en",
                    "slice": "filters",
                    "filters": {"workspace": "alpha"},
                    "expected_source_kind": "text",
                }
            )
            + "\n",
            encoding="utf-8",
        )
        (self.dataset / "qrels.json").write_text(
            json.dumps({"q1": {"allowed": 2}, "q2": {"allowed": 1}}),
            encoding="utf-8",
        )
        rankings = self.root / "rankings.json"
        rankings.write_text(
            json.dumps(self._external_payload()),
            encoding="utf-8",
        )

        with self.assertRaisesRegex(BenchmarkError, r"missing=\['q2'\]"):
            run_benchmark(dataset_dir=self.dataset, rankings_path=rankings)

    def test_artifacts_are_semantically_deterministic_except_timestamp(self) -> None:
        first = run_benchmark(
            dataset_dir=self.dataset,
            generated_at="2026-01-01T00:00:00+00:00",
        )
        second = run_benchmark(
            dataset_dir=self.dataset,
            generated_at="2026-01-02T00:00:00+00:00",
        )

        self.assertTrue(equivalent_ignoring_timestamps(first, second))
        self.assertEqual(first["metrics"]["by_slice"]["filters"]["query_count"], 1)

    def test_cli_returns_nonzero_when_leakage_is_detected(self) -> None:
        rankings = self.root / "rankings.json"
        artifact_path = self.root / "artifact.json"
        rankings.write_text(
            json.dumps(
                self._external_payload(
                    results=[
                        {
                            "doc_id": "forbidden",
                            "citation": "mem://files/forbidden",
                        }
                    ]
                )
            ),
            encoding="utf-8",
        )

        with redirect_stdout(StringIO()), redirect_stderr(StringIO()):
            exit_code = main(
                [
                    "run",
                    "--dataset",
                    str(self.dataset),
                    "--rankings",
                    str(rankings),
                    "--output",
                    str(artifact_path),
                ]
            )

        self.assertEqual(exit_code, 2)
        self.assertEqual(
            json.loads(artifact_path.read_text(encoding="utf-8"))["metrics"]["overall"][
                "leakage_count"
            ],
            1,
        )


class CheckedInFixtureTest(unittest.TestCase):
    def test_dataset_has_every_required_slice_language_and_source_kind(self) -> None:
        artifact = run_benchmark(
            dataset_dir=RECALL_ROOT / "data" / "v1",
            generated_at="2000-01-01T00:00:00+00:00",
        )

        self.assertEqual(
            set(artifact["metrics"]["by_slice"]),
            {"exact", "filters", "hard-negative", "paraphrase"},
        )
        self.assertEqual(set(artifact["metrics"]["by_language"]), {"en", "zh"})
        self.assertEqual(
            set(artifact["metrics"]["by_source_kind"]),
            {"structured", "text", "image_caption"},
        )
        self.assertEqual(artifact["metrics"]["overall"]["leakage_count"], 0)
        serialized = json.dumps(artifact, ensure_ascii=False)
        self.assertNotIn("How should Jordan open the weekly update?", serialized)
        self.assertNotIn("Cassini observed Saturn", serialized)

        comparison = compare_artifacts(artifact, artifact)
        self.assertEqual(
            set(comparison["breakdowns"]["by_slice"]),
            {"exact", "filters", "hard-negative", "paraphrase"},
        )
        self.assertEqual(
            comparison["breakdowns"]["by_slice"]["filters"]["delta"]["recall_at_1"],
            0.0,
        )

    def test_provider_agnostic_example_is_a_complete_valid_ranking(self) -> None:
        artifact = run_benchmark(
            dataset_dir=RECALL_ROOT / "data" / "v1",
            rankings_path=RECALL_ROOT
            / "fixtures"
            / "external-rankings.example.v1.json",
            generated_at="2000-01-01T00:00:00+00:00",
        )

        self.assertEqual(artifact["engine"], "external-example")
        self.assertEqual(artifact["metrics"]["overall"]["leakage_count"], 0)
        self.assertEqual(artifact["metrics"]["overall"]["recall_at_1"], 1.0)

    def test_checked_in_malicious_fixture_exits_nonzero(self) -> None:
        with tempfile.TemporaryDirectory() as tempdir:
            output = Path(tempdir) / "leak.json"
            with redirect_stdout(StringIO()), redirect_stderr(StringIO()):
                exit_code = main(
                    [
                        "run",
                        "--dataset",
                        str(RECALL_ROOT / "data" / "v1"),
                        "--rankings",
                        str(
                            RECALL_ROOT / "fixtures" / "external-rankings.leak.v1.json"
                        ),
                        "--output",
                        str(output),
                    ]
                )

            self.assertEqual(exit_code, 2)
            self.assertEqual(
                json.loads(output.read_text(encoding="utf-8"))["metrics"]["overall"][
                    "leakage_count"
                ],
                1,
            )


if __name__ == "__main__":
    unittest.main()
