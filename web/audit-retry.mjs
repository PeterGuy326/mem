import { spawnSync } from "node:child_process";
import { setTimeout as sleep } from "node:timers/promises";
import { pathToFileURL } from "node:url";

const MAX_ATTEMPTS = 3;
const BACKOFF_MS = 10_000;
// At most six attempts and four backoffs across both audit thresholds (6m40s).
const ATTEMPT_TIMEOUT_MS = 60_000;

const NETWORK_PATTERNS = [
  "network timeout",
  "503 Service Unavailable",
  "ECONNRESET",
  "ETIMEDOUT",
];

const VULNERABILITY_PATTERNS = [
  "found \\d+ vulnerabilit(?:y|ies)",
  "npm audit report",
  "vulnerabilities found",
];

const COMMANDS = [
  {
    label: "production dependencies (moderate threshold)",
    args: ["audit", "--omit=dev", "--audit-level=moderate"],
  },
  {
    label: "all dependencies (high threshold)",
    args: ["audit", "--audit-level=high"],
  },
];

function isNetworkError(stderr) {
  return NETWORK_PATTERNS.some((p) => stderr.toLowerCase().includes(p.toLowerCase()));
}

function isVulnerabilityReport(stdout) {
  return VULNERABILITY_PATTERNS.some((p) => new RegExp(p, "i").test(stdout));
}

async function runWithRetry(label, args, { spawn, wait, npmExecPath, stdout, stderr }) {
  let result;
  const finish = () => {
    stdout.write(result.stdout ?? "");
    stderr.write(result.stderr ?? "");
    if (result.error) {
      stderr.write(`[audit-retry] ${result.error.code ?? "spawn error"}: ${result.error.message}\n`);
    }
    if (result.signal) {
      stderr.write(`[audit-retry] terminated by ${result.signal}.\n`);
    }
    return Number.isInteger(result.status) && result.status > 0 && result.status <= 255 ? result.status : 1;
  };

  for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
    const ts = new Date().toISOString();
    stderr.write(
      `[audit-retry] ${ts} — ${label} (attempt ${attempt}/${MAX_ATTEMPTS})\n`
    );

    // npm run supplies the CLI path, including on Windows where npm is a .cmd
    // shim that cannot be launched directly with shell-free spawnSync.
    result = spawn(npmExecPath ? process.execPath : "npm", npmExecPath ? [npmExecPath, ...args] : args, {
      encoding: "utf8",
      stdio: ["inherit", "pipe", "pipe"],
      timeout: ATTEMPT_TIMEOUT_MS,
      killSignal: "SIGKILL",
    });

    // An interrupted or unstarted audit is never a valid audit result, even
    // when its partial output happens to mention a transient network error.
    if (result.error || result.signal || !Number.isInteger(result.status) || result.status < 0 || result.status > 255) {
      stderr.write(`[audit-retry] ${label} did not complete — not retrying.\n`);
      return finish();
    }

    if (result.status === 0) {
      stderr.write(`[audit-retry] ${label} passed.\n`);
      return 0;
    }

    const output = `${result.stdout ?? ""}\n${result.stderr ?? ""}`;

    if (isVulnerabilityReport(output)) {
      stderr.write(
        `[audit-retry] ${label} found real vulnerabilities — not retrying.\n`
      );
      return finish();
    }

    if (isNetworkError(output)) {
      if (attempt < MAX_ATTEMPTS) {
        stderr.write(`[audit-retry] ${label} hit a network error — will retry.\n`);
        await wait(BACKOFF_MS);
        continue;
      }
      stderr.write(`[audit-retry] ${label} exhausted ${MAX_ATTEMPTS} attempts.\n`);
      return finish();
    }

    stderr.write(
      `[audit-retry] ${label} failed with unrecognized error — not retrying.\n`
    );
    return finish();
  }
}

export async function runAudits({
  spawn = spawnSync,
  wait = sleep,
  npmExecPath = process.env.npm_execpath,
  platform = process.platform,
  stdout = process.stdout,
  stderr = process.stderr,
} = {}) {
  if (platform === "win32" && !npmExecPath) {
    stderr.write("[audit-retry] Run npm run audit so npm supplies its CLI path on Windows.\n");
    return 1;
  }

  for (const cmd of COMMANDS) {
    const code = await runWithRetry(cmd.label, cmd.args, { spawn, wait, npmExecPath, stdout, stderr });
    if (code !== 0) return code;
  }
  return 0;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = await runAudits();
}
