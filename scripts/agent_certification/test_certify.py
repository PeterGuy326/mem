#!/usr/bin/env python3
"""Regression tests for the Agent-host certification machinery."""

from __future__ import annotations

import contextlib
import io
import json
import os
import sys
import tempfile
import unittest
import urllib.error
import urllib.request
from pathlib import Path
from unittest import mock

import certify
import stdio_trace


class FixtureTests(unittest.TestCase):
    def test_every_host_fixture_and_manifest_parses(self) -> None:
        manifests = certify.validate_fixtures()
        self.assertEqual(
            [manifest["host_id"] for manifest in manifests],
            list(certify.HOST_IDS),
        )
        for manifest in manifests:
            self.assertEqual(manifest["transport"], "stdio")
            self.assertIn(manifest["status"], certify.STATUS_ORDER)

    def test_checked_in_material_has_no_token_or_private_path(self) -> None:
        for root in (certify.FIXTURE_DIR, certify.MANIFEST_DIR):
            for path in root.iterdir():
                if not path.is_file():
                    continue
                source = path.read_text(encoding="utf-8")
                self.assertNotIn("/Users/", source)
                self.assertNotIn("/home/", source)
                for secret in certify.SECRET_MARKERS:
                    self.assertNotIn(secret, source)

    def test_codex_fallback_parser_rejects_unknown_section(self) -> None:
        with mock.patch.object(certify, "tomllib", None):
            with self.assertRaises(certify.CertificationError):
                certify._parse_codex_toml("[other]\ncommand = \"mem-mcp\"\n")

    def test_codex_stdlib_parser_path_rejects_unknown_section(self) -> None:
        class ParsedOtherSection:
            @staticmethod
            def loads(_source: str) -> dict[str, object]:
                return {"other": {"command": "mem-mcp"}}

        with mock.patch.object(certify, "tomllib", ParsedOtherSection):
            with self.assertRaises(certify.CertificationError):
                certify._parse_codex_toml("[other]\ncommand = \"mem-mcp\"\n")


class FakeMemdTests(unittest.TestCase):
    def _request(
        self,
        url: str,
        *,
        token: str | None,
        workspace: str,
        body: dict[str, object],
    ) -> tuple[int, dict[str, object] | str]:
        headers = {
            "Content-Type": "application/json",
            "X-Workspace-ID": workspace,
        }
        if token is not None:
            headers["Authorization"] = "Bearer " + token
        request = urllib.request.Request(
            url,
            data=json.dumps(body).encode("utf-8"),
            headers=headers,
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=2) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as exc:
            return exc.code, json.load(exc)

    def test_invalid_token_and_insufficient_role_are_distinct(self) -> None:
        with certify.FakeMemd() as fake:
            status, body = self._request(
                fake.url + "/v1/search",
                token=certify.INVALID_TOKEN,
                workspace=certify.WORKSPACE_A,
                body={"query": "x"},
            )
            self.assertEqual(status, 401)
            self.assertEqual(body["error"], "invalid_token")
            status, body = self._request(
                fake.url + "/v1/memories",
                token=certify.READ_ONLY_TOKEN,
                workspace=certify.WORKSPACE_A,
                body={"content": "x"},
            )
            self.assertEqual(status, 403)
            self.assertEqual(body["error"], "insufficient_role")

    def test_partial_context_keeps_typed_warning(self) -> None:
        with certify.FakeMemd() as fake:
            status, body = self._request(
                fake.url + "/v1/context",
                token=certify.WRITE_TOKEN,
                workspace=certify.WORKSPACE_A,
                body={"query": "__partial__"},
            )
            self.assertEqual(status, 200)
            self.assertIs(body["partial"], True)
            self.assertEqual(body["warnings"][0]["code"], "lane_unavailable")


class ProcessBoundaryTests(unittest.TestCase):
    fixture = certify.FIXTURE_DIR / "fake_mcp.py"

    def _client(self, mode: str, timeout: float = 1) -> certify.AdapterProcess:
        return certify.AdapterProcess(
            Path(sys.executable),
            args=[str(self.fixture)],
            server_url="http://127.0.0.1:1",
            token=None,
            workspace=None,
            response_timeout=timeout,
            process_env={"CERT_FAKE_MODE": mode},
        )

    def test_protocol_client_accepts_clean_stdout(self) -> None:
        with self._client("good") as client:
            client.initialize()
            value = certify.tool_json(client.call("mem_search", {"query": "x"}))
            self.assertEqual(value, {"results": []})

    def test_protocol_client_rejects_stdout_pollution(self) -> None:
        client = self._client("polluted")
        try:
            with self.assertRaises(certify.CertificationError):
                client.request("initialize", {})
        finally:
            client.close()
        with self.assertRaises(certify.CertificationError):
            client.assert_clean()

    def test_protocol_client_rejects_foreign_id_and_invalid_frames(self) -> None:
        for mode in ("foreign-id", "non-object", "invalid-frame"):
            with self.subTest(mode=mode):
                client = self._client(mode)
                try:
                    with self.assertRaises(certify.CertificationError):
                        client.request("initialize", {})
                finally:
                    client.close()

    def test_timeout_terminates_process_group(self) -> None:
        client = self._client("hang", timeout=0.1)
        pid = client.pid
        try:
            with self.assertRaises(certify.CertificationTimeout):
                client.request("initialize", {}, timeout=0.1)
        finally:
            client.close()
            client.assert_clean()
        self.assertFalse(certify._pid_alive(pid))

    def test_sanitizer_redacts_tokens_loopback_and_roots(self) -> None:
        raw = (
            certify.WRITE_TOKEN
            + " http://127.0.0.1:12345 "
            + str(certify.REPOSITORY_ROOT)
        )
        value = certify._sanitize_text(raw, ())
        self.assertNotIn(certify.WRITE_TOKEN, value)
        self.assertNotIn("12345", value)
        self.assertNotIn(str(certify.REPOSITORY_ROOT), value)

    def test_cli_failure_json_sanitizes_token_and_private_path(self) -> None:
        missing = Path.cwd() / (certify.WRITE_TOKEN + "-missing")
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            exit_code = certify.main(
                ["contract", "--mcp-binary", str(missing)]
            )
        self.assertEqual(exit_code, 1)
        output = stderr.getvalue()
        self.assertNotIn(certify.WRITE_TOKEN, output)
        self.assertNotIn(str(Path.cwd()), output)

    def test_stdio_trace_allowlists_method_and_never_records_id_value(self) -> None:
        secret = certify.WRITE_TOKEN
        unknown = json.dumps(
            {"jsonrpc": "2.0", "id": secret, "method": secret}
        ).encode("utf-8")
        label = stdio_trace._label("host->adapter", unknown)
        self.assertEqual(
            label,
            "host->adapter method=<unknown> id=present",
        )
        self.assertNotIn(secret, label)
        known = json.dumps(
            {"jsonrpc": "2.0", "id": "safe-but-not-recorded", "method": "tools/list"}
        ).encode("utf-8")
        self.assertEqual(
            stdio_trace._label("host->adapter", known),
            "host->adapter method=tools/list id=present",
        )

    def test_bounded_runner_rejects_secret_in_argv(self) -> None:
        with tempfile.TemporaryDirectory(prefix="mem-cert-runner-") as raw:
            with self.assertRaises(certify.CertificationError):
                certify._run_bounded(
                    [sys.executable, "-c", "pass", certify.WRITE_TOKEN],
                    env=certify._safe_process_env(),
                    cwd=Path(raw),
                    name="secret-argv",
                    proves="NOT RUN",
                )

    @unittest.skipUnless(os.name == "posix", "process-group test requires POSIX")
    def test_bounded_runner_reaps_orphaned_child(self) -> None:
        fixture = certify.FIXTURE_DIR / "spawn_orphan.py"
        with tempfile.TemporaryDirectory(prefix="mem-cert-runner-") as raw:
            item = certify._run_bounded(
                [sys.executable, str(fixture)],
                env=certify._safe_process_env(),
                cwd=Path(raw),
                name="orphan",
                proves="NOT RUN",
            )
        self.assertEqual(item.outcome, "PASS")
        child_pid = int(item.output.strip())
        self.assertFalse(certify._pid_alive(child_pid))

    def test_generated_host_configs_never_materialize_token(self) -> None:
        manifests = {
            item["host_id"]: item for item in certify.validate_fixtures()
        }
        replacements = {
            "MEM_MCP_COMMAND": "/tmp/mem-mcp",
            "MEM_SERVER": "http://127.0.0.1:1",
            "MEM_TOKEN": certify.WRITE_TOKEN,
            "MEM_WORKSPACE": certify.WORKSPACE_A,
        }
        for host_id in ("openclaw", "claude-code", "opencode"):
            with self.subTest(host=host_id), tempfile.TemporaryDirectory(
                prefix="mem-cert-config-"
            ) as raw:
                host_root = Path(raw)
                env = certify._safe_process_env(
                    {
                        "MEM_SERVER": replacements["MEM_SERVER"],
                        "MEM_TOKEN": replacements["MEM_TOKEN"],
                        "MEM_WORKSPACE": replacements["MEM_WORKSPACE"],
                        "MEM_MCP_COMMAND": replacements["MEM_MCP_COMMAND"],
                    }
                )
                certify._prepare_real_probe(
                    host_id,
                    Path(sys.executable),
                    manifests[host_id],
                    host_root,
                    env,
                )
                for path in host_root.rglob("*"):
                    if path.is_file():
                        source = path.read_text(encoding="utf-8")
                        for secret in certify.SECRET_MARKERS:
                            self.assertNotIn(secret, source)

    def test_opencode_discovery_validator_rejects_negative_states(self) -> None:
        command = {
            "name": "mcp-list",
            "validator": "opencode-discovered",
        }
        for output in (
            "mem disconnected",
            "mem not connected",
            "mem failed",
            "other connected\nmem error",
        ):
            with self.subTest(output=output):
                item = certify.CommandEvidence(
                    "mcp-list",
                    ["opencode", "mcp", "list"],
                    0,
                    "PASS",
                    "DISCOVERED",
                    output,
                )
                validated, _ = certify._validate_host_evidence(
                    "opencode", command, item, Path("/tmp/mem-mcp"), []
                )
                self.assertFalse(validated)
        item = certify.CommandEvidence(
            "mcp-list",
            ["opencode", "mcp", "list"],
            0,
            "PASS",
            "DISCOVERED",
            "● mem connected",
        )
        validated, error = certify._validate_host_evidence(
            "opencode", command, item, Path("/tmp/mem-mcp"), []
        )
        self.assertTrue(validated, error)

    def test_openclaw_and_claude_validators_reject_negative_states(self) -> None:
        binary = Path("/tmp/mem-mcp")
        openclaw_command = {
            "name": "mcp-list",
            "validator": "openclaw-registered",
        }
        for output in (
            '{"servers":[{"name":"mem","command":"/tmp/mem-mcp","status":"error"}]}',
            '{"error":"mem-mcp not found","name":"mem","command":"/tmp/mem-mcp"}',
        ):
            item = certify.CommandEvidence(
                "mcp-list",
                ["openclaw", "mcp", "list"],
                0,
                "PASS",
                "REGISTERED",
                output,
            )
            validated, _ = certify._validate_host_evidence(
                "openclaw", openclaw_command, item, binary, []
            )
            self.assertFalse(validated)
        positive = certify.CommandEvidence(
            "mcp-list",
            ["openclaw", "mcp", "list"],
            0,
            "PASS",
            "REGISTERED",
            '{"servers":[{"name":"mem","command":"/tmp/mem-mcp"}]}',
        )
        validated, error = certify._validate_host_evidence(
            "openclaw", openclaw_command, positive, binary, []
        )
        self.assertTrue(validated, error)

        claude_command = {
            "name": "mcp-list",
            "validator": "claude-registered",
        }
        for output in (
            "mem: /tmp/mem-mcp - error",
            "mem: /tmp/mem-mcp - not connected",
            "mem: /tmp/mem-mcp - pending approval",
        ):
            item = certify.CommandEvidence(
                "mcp-list",
                ["claude", "mcp", "list"],
                0,
                "PASS",
                "REGISTERED",
                output,
            )
            validated, _ = certify._validate_host_evidence(
                "claude-code", claude_command, item, binary, []
            )
            self.assertFalse(validated)
        positive = certify.CommandEvidence(
            "mcp-list",
            ["claude", "mcp", "list"],
            0,
            "PASS",
            "REGISTERED",
            "mem: /tmp/mem-mcp - connected",
        )
        validated, error = certify._validate_host_evidence(
            "claude-code", claude_command, positive, binary, []
        )
        self.assertTrue(validated, error)

    def test_bounded_runner_caps_retained_output(self) -> None:
        with tempfile.TemporaryDirectory(prefix="mem-cert-output-") as raw:
            item = certify._run_bounded(
                [
                    sys.executable,
                    "-c",
                    "import sys; sys.stdout.write('x' * 100000)",
                ],
                env=certify._safe_process_env(),
                cwd=Path(raw),
                name="bounded-output",
                proves="NOT RUN",
            )
        self.assertEqual(item.outcome, "PASS")
        self.assertIn("earlier output truncated", item.output)
        self.assertLess(
            len(item.output.encode("utf-8")),
            certify.MAX_COMMAND_OUTPUT_BYTES + 100,
        )

    def test_not_run_reason_includes_validator_failure(self) -> None:
        evidence = [
            certify.CommandEvidence(
                "mcp-list",
                ["opencode", "mcp", "list"],
                0,
                "PASS",
                "DISCOVERED",
                "No MCP servers",
                False,
                "OpenCode list output did not identify mem",
            )
        ]
        reason = certify._real_host_reason("opencode", "NOT RUN", evidence)
        self.assertIn("did not identify mem", reason)


class CurrentAdapterContractTests(unittest.TestCase):
    def test_current_adapter_when_explicitly_requested(self) -> None:
        raw = os.environ.get("MEM_MCP_CERT_BINARY")
        if not raw:
            self.skipTest("set MEM_MCP_CERT_BINARY for the current-adapter contract")
        result = certify.run_contract(Path(raw))
        self.assertEqual(result["result"], "PASS")
        self.assertIn("timeout-cleanup", result["checks"])
        self.assertIn("path-with-spaces", result["checks"])


if __name__ == "__main__":
    unittest.main()
