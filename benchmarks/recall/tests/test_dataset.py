from __future__ import annotations

from dataclasses import replace
import json
from pathlib import Path
import tempfile
import unittest

from benchmarks.recall.dataset import Document, document_matches_filters, load_dataset
from benchmarks.recall.errors import BenchmarkError


RECALL_ROOT = Path(__file__).resolve().parents[1]


class FilterSemanticsTest(unittest.TestCase):
    def setUp(self) -> None:
        self.document = Document(
            id="doc",
            language="en",
            source_kind="text",
            workspace="alpha",
            path="/notes/item.md",
            citation="mem://files/doc",
            text="synthetic",
            metadata={},
        )

    def test_path_prefix_matches_complete_path_segments(self) -> None:
        self.assertTrue(
            document_matches_filters(self.document, {"path_prefix": "/notes"})
        )
        self.assertTrue(
            document_matches_filters(self.document, {"path_prefix": "/notes/"})
        )
        self.assertTrue(
            document_matches_filters(
                replace(self.document, path="/notes"),
                {"path_prefix": "/notes"},
            )
        )

    def test_path_prefix_does_not_match_similar_sibling(self) -> None:
        for path in ("/notes-secret/item.md", "/notes2/item.md"):
            with self.subTest(path=path):
                self.assertFalse(
                    document_matches_filters(
                        replace(self.document, path=path),
                        {"path_prefix": "/notes"},
                    )
                )

    def test_root_prefix_matches_absolute_paths(self) -> None:
        self.assertTrue(document_matches_filters(self.document, {"path_prefix": "/"}))


class DatasetValidationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = Path(self.tempdir.name)
        (self.root / "dataset.json").write_text(
            json.dumps(
                {
                    "schema_version": "mem.recall-dataset.v1",
                    "version": "validation-v1",
                    "provenance": "hand-authored synthetic data",
                    "license": "CC0-1.0",
                    "required_coverage": {
                        "slices": ["exact"],
                        "languages": ["en"],
                        "source_kinds": ["text"],
                    },
                }
            ),
            encoding="utf-8",
        )
        self.document = {
            "id": "doc",
            "language": "en",
            "source_kind": "text",
            "workspace": "alpha",
            "path": "/notes/doc.md",
            "citation": "mem://files/doc",
            "text": "synthetic recall text",
            "provenance": "synthetic",
        }
        (self.root / "queries.jsonl").write_text(
            json.dumps(
                {
                    "id": "q",
                    "text": "recall",
                    "language": "en",
                    "slice": "exact",
                    "filters": {"workspace": "alpha"},
                    "expected_source_kind": "text",
                }
            )
            + "\n",
            encoding="utf-8",
        )

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def _write_corpus_and_qrels(
        self,
        *,
        document: dict[str, object] | None = None,
        grade: int = 1,
    ) -> None:
        (self.root / "corpus.jsonl").write_text(
            json.dumps(document or self.document) + "\n",
            encoding="utf-8",
        )
        (self.root / "qrels.json").write_text(
            json.dumps({"q": {"doc": grade}}),
            encoding="utf-8",
        )

    def test_rejects_non_synthetic_corpus_provenance(self) -> None:
        document = {**self.document, "provenance": "private-export"}
        self._write_corpus_and_qrels(document=document)

        with self.assertRaisesRegex(BenchmarkError, "provenance"):
            load_dataset(self.root)

    def test_rejects_relevant_document_outside_query_filter(self) -> None:
        document = {**self.document, "workspace": "beta"}
        self._write_corpus_and_qrels(document=document)

        with self.assertRaisesRegex(BenchmarkError, "filter-ineligible"):
            load_dataset(self.root)

    def test_rejects_qrel_grade_outside_documented_zero_to_three_scale(self) -> None:
        self._write_corpus_and_qrels(grade=4)

        with self.assertRaisesRegex(BenchmarkError, "from 0 to 3"):
            load_dataset(self.root)

    def test_rejects_path_traversal_in_corpus(self) -> None:
        document = {**self.document, "path": "/notes/../private.md"}
        self._write_corpus_and_qrels(document=document)

        with self.assertRaisesRegex(BenchmarkError, "absolute clean path"):
            load_dataset(self.root)


class CheckedInProfileTextFixtureTest(unittest.TestCase):
    def test_profile_text_fixture_is_file_only_and_loadable(self) -> None:
        dataset = load_dataset(RECALL_ROOT / "data" / "profile-text-v1")

        self.assertEqual(dataset.metadata["version"], "synthetic-profile-text-v1")
        self.assertEqual(len(dataset.documents), 5)
        self.assertEqual(len(dataset.queries), 4)
        self.assertEqual({document.source_kind for document in dataset.documents}, {"text"})
        self.assertEqual(
            set(dataset.metadata["required_coverage"]["slices"]),
            {"exact", "paraphrase", "filters", "hard-negative"},
        )


if __name__ == "__main__":
    unittest.main()
