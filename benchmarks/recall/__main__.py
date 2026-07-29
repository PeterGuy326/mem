from __future__ import annotations

import argparse
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path
import sys
import tempfile

from .errors import BenchmarkError
from .runner import (
    compare_artifacts,
    comparison_summary,
    equivalent_ignoring_timestamps,
    human_summary,
    load_artifact,
    run_benchmark,
    write_json,
)


PACKAGE_ROOT = Path(__file__).resolve().parent
DEFAULT_DATASET = PACKAGE_ROOT / "data" / "v1"
DEFAULT_BASELINE = PACKAGE_ROOT / "baselines" / "lexical-reference.v1.json"


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="python -m benchmarks.recall",
        description="Offline multilingual recall benchmark (not production recall).",
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    run = subparsers.add_parser("run", help="run lexical or external rankings")
    run.add_argument("--dataset", type=Path, default=DEFAULT_DATASET)
    run.add_argument(
        "--rankings",
        type=Path,
        help="opt-in external rankings JSON; omission uses lexical-reference",
    )
    run.add_argument("--output", type=Path, required=True)
    run.add_argument(
        "--compare",
        type=Path,
        help="write informational deltas against this baseline beside the artifact",
    )
    run.add_argument(
        "--comparison-output",
        type=Path,
        help="comparison JSON path (defaults to <output>.comparison.json)",
    )

    compare = subparsers.add_parser("compare", help="compare two artifacts")
    compare.add_argument("--baseline", type=Path, default=DEFAULT_BASELINE)
    compare.add_argument("--candidate", type=Path, required=True)
    compare.add_argument("--output", type=Path, required=True)

    verify = subparsers.add_parser(
        "verify",
        help="run deterministic, baseline-comparison, and leakage self-checks",
    )
    verify.add_argument("--dataset", type=Path, default=DEFAULT_DATASET)
    verify.add_argument("--baseline", type=Path, default=DEFAULT_BASELINE)
    verify.add_argument(
        "--leak-rankings",
        type=Path,
        default=PACKAGE_ROOT / "fixtures" / "external-rankings.leak.v1.json",
    )
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        if args.command == "run":
            artifact = run_benchmark(
                dataset_dir=args.dataset,
                rankings_path=args.rankings,
            )
            write_json(args.output, artifact)
            print(human_summary(artifact))
            if args.compare:
                comparison = compare_artifacts(load_artifact(args.compare), artifact)
                comparison_output = args.comparison_output or args.output.with_suffix(
                    ".comparison.json"
                )
                write_json(comparison_output, comparison)
                print(comparison_summary(comparison))
                print(f"comparison artifact: {comparison_output}")
            if artifact["metrics"]["overall"]["leakage_count"]:
                print("FAILED: forbidden-source leakage detected", file=sys.stderr)
                return 2
            return 0

        if args.command == "compare":
            baseline = load_artifact(args.baseline)
            candidate = load_artifact(args.candidate)
            comparison = compare_artifacts(baseline, candidate)
            write_json(args.output, comparison)
            print(comparison_summary(comparison))
            return 2 if candidate["metrics"]["overall"]["leakage_count"] else 0

        first = run_benchmark(
            dataset_dir=args.dataset,
            generated_at="2000-01-01T00:00:00+00:00",
        )
        second = run_benchmark(
            dataset_dir=args.dataset,
            generated_at="2000-01-02T00:00:00+00:00",
        )
        if not equivalent_ignoring_timestamps(first, second):
            raise BenchmarkError(
                "two lexical-reference artifacts differ beyond generated_at"
            )
        comparison = compare_artifacts(load_artifact(args.baseline), first)
        leak_artifact = run_benchmark(
            dataset_dir=args.dataset,
            rankings_path=args.leak_rankings,
            generated_at="2000-01-01T00:00:00+00:00",
        )
        if not leak_artifact["metrics"]["overall"]["leakage_count"]:
            raise BenchmarkError("malicious leakage fixture was not detected")
        with tempfile.TemporaryDirectory(prefix="mem-recall-verify-") as tempdir:
            leak_path = Path(tempdir) / "leak.json"
            write_json(leak_path, leak_artifact)
            with redirect_stdout(StringIO()), redirect_stderr(StringIO()):
                leak_exit = main(
                    [
                        "compare",
                        "--baseline",
                        str(args.baseline),
                        "--candidate",
                        str(leak_path),
                        "--output",
                        str(Path(tempdir) / "leak-comparison.json"),
                    ]
                )
        if leak_exit == 0:
            raise BenchmarkError("leakage comparison unexpectedly exited zero")
        print("determinism: PASS (only generated_at differs)")
        print(comparison_summary(comparison))
        print(
            "leakage gate: PASS "
            f"({leak_artifact['metrics']['overall']['leakage_count']} forbidden "
            "result detected; non-zero exit observed)"
        )
        return 0
    except BenchmarkError as exc:
        print(f"benchmark input error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
