// @vitest-environment node
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it, vi } from "vitest";
import { runAudits } from "./audit-retry.mjs";

const PASS = { status: 0, stdout: "", stderr: "" };
const PROD_ARGS = ["audit", "--omit=dev", "--audit-level=moderate"];
const ALL_ARGS = ["audit", "--audit-level=high"];

function harness(results, overrides = {}) {
  let stdout = "";
  let stderr = "";
  const spawn = vi.fn(() => {
    const result = results.shift();
    if (!result) throw new Error("Unexpected audit attempt");
    return result;
  });
  const wait = vi.fn(async () => {});
  return {
    spawn,
    wait,
    output: () => ({ stdout, stderr }),
    run: () => runAudits({
      spawn,
      wait,
      npmExecPath: "/npm with spaces/npm-cli.js",
      stdout: { write: (text) => { stdout += text; } },
      stderr: { write: (text) => { stderr += text; } },
      ...overrides,
    }),
  };
}

describe("audit retry policy", () => {
  it("requires both unchanged thresholds to pass, using Node and the npm CLI path", async () => {
    const test = harness([PASS, PASS], { platform: "win32" });
    expect(await test.run()).toBe(0);
    expect(test.spawn.mock.calls.map(([command, args]) => [command, args])).toEqual([
      [process.execPath, ["/npm with spaces/npm-cli.js", ...PROD_ARGS]],
      [process.execPath, ["/npm with spaces/npm-cli.js", ...ALL_ARGS]],
    ]);
    for (const [, , options] of test.spawn.mock.calls) {
      expect(options).toMatchObject({ timeout: 60_000, killSignal: "SIGKILL" });
      expect(options.shell).toBeUndefined();
    }
    expect(test.wait).not.toHaveBeenCalled();
  });

  it("keeps direct Node invocation available on Unix", async () => {
    const test = harness([PASS, PASS], { npmExecPath: "", platform: "linux" });
    expect(await test.run()).toBe(0);
    expect(test.spawn.mock.calls[0].slice(0, 2)).toEqual(["npm", PROD_ARGS]);
  });

  it("fails with an actionable message when direct invocation lacks npm on Windows", async () => {
    const test = harness([], { npmExecPath: "", platform: "win32" });
    expect(await test.run()).toBe(1);
    expect(test.output().stderr).toContain("Run npm run audit");
    expect(test.spawn).not.toHaveBeenCalled();
  });

  it.each(["network timeout", "503 Service Unavailable", "econnreset", "ETIMEDOUT"])(
    "retries a recognized transient failure: %s", async (message) => {
      const test = harness([{ status: 1, stderr: message }, PASS, PASS]);
      expect(await test.run()).toBe(0);
      expect(test.spawn).toHaveBeenCalledTimes(3);
      expect(test.wait.mock.calls).toEqual([[10_000]]);
    },
  );

  it("recognizes transient failures on stdout", async () => {
    const test = harness([{ status: 1, stdout: "ECONNRESET" }, PASS, PASS]);
    expect(await test.run()).toBe(0);
    expect(test.wait).toHaveBeenCalledTimes(1);
  });

  it("caps retries at three, skips the final backoff, and retains final diagnostics", async () => {
    const failure = { status: 7, stdout: "final stdout\n", stderr: "503 Service Unavailable: final detail\n" };
    const test = harness([failure, failure, failure]);
    expect(await test.run()).toBe(7);
    expect(test.spawn).toHaveBeenCalledTimes(3);
    expect(test.wait.mock.calls).toEqual([[10_000], [10_000]]);
    expect(test.output().stdout).toBe(failure.stdout);
    expect(test.output().stderr).toContain(failure.stderr);
    expect(test.output().stderr).toContain("exhausted 3 attempts");
    expect(test.output().stderr.match(/will retry/g)).toHaveLength(2);
  });

  it("gives the second threshold its own bounded retry budget", async () => {
    const failure = { status: 1, stderr: "ETIMEDOUT" };
    const test = harness([failure, failure, PASS, failure, failure, PASS]);
    expect(await test.run()).toBe(0);
    expect(test.spawn).toHaveBeenCalledTimes(6);
    expect(test.wait.mock.calls).toEqual(Array(4).fill([10_000]));
    expect(test.spawn.mock.calls[3][1].slice(1)).toEqual(ALL_ARGS);
  });

  it.each(["stdout", "stderr"])("never retries vulnerabilities on %s, even with network text", async (stream) => {
    const test = harness([{ status: 1, [stream]: "# npm audit report\nETIMEDOUT\n" }]);
    expect(await test.run()).toBe(1);
    expect(test.spawn).toHaveBeenCalledTimes(1);
    expect(test.wait).not.toHaveBeenCalled();
    expect(test.output()[stream]).toContain("# npm audit report");
  });

  it.each(["found 1 vulnerability", "found 2 vulnerabilities", "vulnerabilities found"])(
    "prioritizes vulnerability summaries over network text: %s", async (message) => {
      const test = harness([{ status: 1, stdout: message, stderr: "ECONNRESET" }]);
      expect(await test.run()).toBe(1);
      expect(test.spawn).toHaveBeenCalledTimes(1);
      expect(test.wait).not.toHaveBeenCalled();
    },
  );

  it.each(["E401 unauthorized", "invalid config", "fetch failed", "audit endpoint returned an error"])(
    "fails unknown or non-transient errors without retry: %s", async (message) => {
      const test = harness([{ status: 2, stdout: "diagnostic\n", stderr: message }]);
      expect(await test.run()).toBe(2);
      expect(test.wait).not.toHaveBeenCalled();
      expect(test.output().stdout).toBe("diagnostic\n");
      expect(test.output().stderr).toContain(message);
    },
  );

  it.each([
    ["missing executable", { status: null, error: Object.assign(new Error("npm missing"), { code: "ENOENT" }) }, "ENOENT"],
    ["timeout", { status: null, error: Object.assign(new Error("timed out"), { code: "ETIMEDOUT" }) }, "timed out"],
    ["signal", { status: null, signal: "SIGTERM" }, "SIGTERM"],
    ["null status", { status: null }, "did not complete"],
    ["missing status", {}, "did not complete"],
    ["negative status", { status: -1 }, "did not complete"],
    ["out of range status", { status: 256 }, "did not complete"],
    ["buffer overflow", { status: null, error: Object.assign(new Error("output limit"), { code: "ENOBUFS" }) }, "ENOBUFS"],
    ["error with zero status", { status: 0, error: new Error("incomplete") }, "incomplete"],
    ["signal with zero status", { status: 0, signal: "SIGKILL" }, "SIGKILL"],
  ])("fails closed for %s regardless of transient-looking output", async (_label, result, diagnostic) => {
    const test = harness([{ ...result, stderr: "503 Service Unavailable" }]);
    expect(await test.run()).toBe(1);
    expect(test.spawn).toHaveBeenCalledTimes(1);
    expect(test.wait).not.toHaveBeenCalled();
    expect(test.output().stderr).toContain(diagnostic);
    expect(test.output().stderr).toContain("503 Service Unavailable");
  });

  it("fails overall if the second threshold finds vulnerabilities", async () => {
    const test = harness([PASS, { status: 1, stdout: "found 2 vulnerabilities" }]);
    expect(await test.run()).toBe(1);
    expect(test.spawn).toHaveBeenCalledTimes(2);
    expect(test.wait).not.toHaveBeenCalled();
  });
});

describe("audit CLI process behavior", () => {
  const directories = [];
  afterEach(() => {
    for (const directory of directories.splice(0)) rmSync(directory, { recursive: true, force: true });
  });

  function fixture(results) {
    const directory = mkdtempSync(join(tmpdir(), "audit npm fixture "));
    directories.push(directory);
    const cli = join(directory, "npm cli.cjs");
    const log = join(directory, "calls.json");
    writeFileSync(cli, `
      const fs = require('node:fs');
      const log = ${JSON.stringify(log)};
      const calls = fs.existsSync(log) ? JSON.parse(fs.readFileSync(log, 'utf8')) : [];
      const result = ${JSON.stringify(results)}[calls.length];
      calls.push(process.argv.slice(2));
      fs.writeFileSync(log, JSON.stringify(calls));
      process.stdout.write(result.stdout || '');
      process.stderr.write(result.stderr || '');
      process.exitCode = result.status;
    `);
    return { cli, log };
  }

  function invoke(cli) {
    return spawnSync(process.execPath, [fileURLToPath(new URL("./audit-retry.mjs", import.meta.url))], {
      encoding: "utf8",
      timeout: 5_000,
      killSignal: "SIGKILL",
      env: { ...process.env, npm_execpath: cli },
    });
  }

  it("executes a CLI path with spaces and exits zero only after both audits", () => {
    const { cli, log } = fixture([PASS, PASS]);
    const result = invoke(cli);
    expect(result.error).toBeUndefined();
    expect(result.status).toBe(0);
    expect(JSON.parse(readFileSync(log, "utf8"))).toEqual([PROD_ARGS, ALL_ARGS]);
  });

  it.each(["# npm audit report\n", "unknown audit error\n"])("returns failure and output for %s", (message) => {
    const { cli, log } = fixture([{ status: 2, stdout: message, stderr: "detail\n" }]);
    const result = invoke(cli);
    expect(result.error).toBeUndefined();
    expect(result.status).toBe(2);
    expect(result.stdout).toBe(message);
    expect(result.stderr).toContain("detail\n");
    expect(JSON.parse(readFileSync(log, "utf8"))).toEqual([PROD_ARGS]);
  });

  it("exits nonzero when the npm CLI cannot be started", () => {
    const { cli } = fixture([]);
    rmSync(cli);
    const result = invoke(cli);
    expect(result.status).not.toBe(0);
    expect(result.stderr).toContain("MODULE_NOT_FOUND");
  });

  it("terminates a stalled process and fails closed at the attempt timeout", async () => {
    const test = harness([], {
      spawn: (_command, _args, options) => spawnSync(process.execPath, [
        "-e", "process.on('SIGTERM', () => {}); setInterval(() => {}, 1000);",
      ], { ...options, timeout: 200 }),
    });
    expect(await test.run()).toBe(1);
    expect(test.wait).not.toHaveBeenCalled();
    expect(test.output().stderr).toContain("ETIMEDOUT");
    expect(test.output().stderr).toContain("SIGKILL");
  });
});
