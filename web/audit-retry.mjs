import { spawnSync } from "node:child_process";

const MAX_ATTEMPTS = 3;
const BACKOFF_MS = 10_000;

const NETWORK_PATTERNS = [
  "network timeout",
  "503 Service Unavailable",
  "ECONNRESET",
  "ETIMEDOUT",
  "fetch failed",
  "audit endpoint returned an error",
];

const VULNERABILITY_PATTERNS = [
  "found \\d+ vulnerabilities",
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

function runWithRetry(label, args) {
  for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
    const ts = new Date().toISOString();
    process.stderr.write(
      `[audit-retry] ${ts} — ${label} (attempt ${attempt}/${MAX_ATTEMPTS})\n`
    );

    const result = spawnSync("npm", args, {
      encoding: "utf8",
      stdio: ["inherit", "pipe", "pipe"],
    });

    if (result.status === 0) {
      process.stderr.write(`[audit-retry] ${label} passed.\n`);
      return 0;
    }

    const stdout = result.stdout ?? "";
    const stderr = result.stderr ?? "";

    if (isVulnerabilityReport(stdout)) {
      process.stderr.write(
        `[audit-retry] ${label} found real vulnerabilities — not retrying.\n`
      );
      process.stdout.write(stdout);
      process.stderr.write(stderr);
      return result.status;
    }

    if (isNetworkError(stderr)) {
      process.stderr.write(
        `[audit-retry] ${label} hit a network error — will retry.\n`
      );
      if (attempt < MAX_ATTEMPTS) {
        spawnSync("sleep", [String(BACKOFF_MS / 1000)]);
      }
      continue;
    }

    process.stderr.write(
      `[audit-retry] ${label} failed with unrecognized error — not retrying.\n`
    );
    process.stdout.write(stdout);
    process.stderr.write(stderr);
    return result.status;
  }

  process.stderr.write(
    `[audit-retry] ${label} exhausted ${MAX_ATTEMPTS} attempts.\n`
  );
  const final = spawnSync("npm", args, { encoding: "utf8", stdio: "inherit" });
  return final.status ?? 1;
}

let exitCode = 0;

for (const cmd of COMMANDS) {
  const code = runWithRetry(cmd.label, cmd.args);
  if (code !== 0) {
    exitCode = code;
    break;
  }
}

process.exit(exitCode);
