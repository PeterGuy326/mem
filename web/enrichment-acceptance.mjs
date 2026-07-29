import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const webRoot = path.dirname(fileURLToPath(import.meta.url));
const port = Number(process.env.ENRICHMENT_E2E_PORT ?? 5194);
const baseURL = `http://127.0.0.1:${port}`;
const artifactDir = path.join(tmpdir(), `mem-enrichment-acceptance-${process.pid}`);
const fileID = 'hero-2012-yunnan-xiaoming';
const descriptionID = 'a1111111-1111-4111-8111-111111111111';
const rejectTagID = 'a2222222-2222-4222-8222-222222222222';
const conflictTagID = 'a3333333-3333-4333-8333-333333333333';
const rejectedDescriptionFileID = 'reject-description-enrichment-e2e';
const rejectedDescriptionID = 'a7777777-7777-4777-8777-777777777777';
const rejectedDescription = 'Legacy model description to reject.';
const description = '朋友们在大理洱海边留下的一张夏日合影。';
const rejectedTag = '洱海';
const conflictTag = '人物合影';
const offlineFileID = 'offline-enrichment-e2e';
const offlineDetailAPIPath = `/v1/files/${offlineFileID}`;

function startVite() {
  const vite = path.join(webRoot, 'node_modules', 'vite', 'bin', 'vite.js');
  const child = spawn(
    process.execPath,
    [vite, '--host', '127.0.0.1', '--port', String(port), '--strictPort'],
    {
      cwd: webRoot,
      env: { ...process.env, VITE_USE_MOCK: 'true' },
      stdio: ['ignore', 'pipe', 'pipe'],
    },
  );
  let output = '';
  child.stdout.on('data', (chunk) => {
    output += chunk.toString();
  });
  child.stderr.on('data', (chunk) => {
    output += chunk.toString();
  });
  return { child, output: () => output };
}

async function waitForServer(server) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (server.child.exitCode !== null) {
      throw new Error(`Vite exited before startup.\n${server.output()}`);
    }
    try {
      const response = await fetch(baseURL);
      if (response.ok) return;
    } catch {
      // Vite is still starting.
    }
    await new Promise((resolve) => setTimeout(resolve, 150));
  }
  throw new Error(`Timed out waiting for ${baseURL}.\n${server.output()}`);
}

async function stopProcess(child) {
  if (child.exitCode !== null) return;
  child.kill('SIGTERM');
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    new Promise((resolve) => setTimeout(resolve, 2_000)),
  ]);
  if (child.exitCode === null) child.kill('SIGKILL');
}

async function launchBrowser() {
  try {
    return await chromium.launch({ headless: true });
  } catch (error) {
    if (!(error instanceof Error) || !error.message.includes("Executable doesn't exist")) {
      throw error;
    }
    return chromium.launch({ channel: 'chrome', headless: true });
  }
}

async function readJSON(page, url, options) {
  return page.evaluate(
    async ({ requestURL, requestOptions }) => {
      const response = await fetch(requestURL, requestOptions);
      return { status: response.status, body: await response.json() };
    },
    { requestURL: url, requestOptions: options },
  );
}

function decisionOptions(decision, expectedVersion = 1) {
  return {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      decision,
      expected_version: expectedVersion,
    }),
  };
}

const vite = startVite();
let browser;
let failed = false;

try {
  await waitForServer(vite);
  await mkdir(artifactDir, { recursive: true });
  browser = await launchBrowser();
  const context = await browser.newContext({ viewport: { width: 1440, height: 1100 } });
  await context.addInitScript(() => {
    localStorage.setItem('mem.token', 'mock-e2e-token');
    localStorage.setItem(
      'mem.user',
      JSON.stringify({
        id: 'user-1',
        email: 'enrichment-e2e@mem.dev',
        created_at: '2026-01-01T00:00:00Z',
      }),
    );
    localStorage.setItem('mem.lang', 'en');
  });

  const page = await context.newPage();
  page.setDefaultTimeout(12_000);
  page.setDefaultNavigationTimeout(15_000);
  const pageErrors = [];
  const consoleErrors = [];
  const offlineRequestFailures = [];
  let browserPhase = 'normal';
  page.on('pageerror', (error) => pageErrors.push(error.message));
  page.on('console', (message) => {
    if (message.type() === 'error') {
      consoleErrors.push({ message: message.text(), phase: browserPhase });
    }
  });
  page.on('requestfailed', (request) => {
    if (browserPhase === 'offline-detail') {
      offlineRequestFailures.push({
        error: request.failure()?.errorText ?? '',
        url: request.url(),
      });
    }
  });

  await page.goto(`${baseURL}/files/${fileID}`, { waitUntil: 'domcontentloaded' });
  await page.getByText('Pending AI suggestions', { exact: true }).waitFor();

  const detail = await readJSON(page, `/v1/files/${fileID}`);
  assert.equal(detail.status, 200);
  assert.equal(detail.body.index_status, 'partial');
  assert.equal(detail.body.source_metadata.source_kind, 'mobile');
  assert.equal(detail.body.source_metadata.location.label, '洱海西岸');
  assert.equal(detail.body.annotations_truncated, true);
  assert.equal(
    detail.body.annotations.filter((annotation) => annotation.status === 'pending').length,
    3,
  );
  assert.ok(detail.body.annotations.some((annotation) => annotation.status === 'accepted'));
  assert.ok(detail.body.annotations.some((annotation) => annotation.status === 'rejected'));
  assert.ok(detail.body.annotations.some((annotation) => annotation.status === 'superseded'));
  console.log('✓ detail contract includes source, processor, and review state');

  await page.getByText('AI enrichment is partial', { exact: true }).waitFor();
  await page.getByText('Raw AI observation (unconfirmed)', { exact: true }).waitFor();
  await page
    .getByText(
      'Provenance: provider=mock-vlm · processor=image · version=image-enrichment-v2 · source=model',
      { exact: true },
    )
    .first()
    .waitFor();
  await page.getByText('91% confidence', { exact: true }).first().waitFor();
  await page.getByText('mobile · phone photo sync', { exact: true }).waitFor();
  await page
    .getByText('Older review history is omitted from this response.', { exact: true })
    .waitFor();
  const pendingOrder = await page
    .locator('[data-testid^="annotation-"]')
    .evaluateAll((nodes) => nodes.map((node) => node.getAttribute('data-testid')));
  assert.deepEqual(pendingOrder, [
    `annotation-${conflictTagID}`,
    `annotation-${rejectTagID}`,
    `annotation-${descriptionID}`,
  ]);
  await page.screenshot({
    path: path.join(artifactDir, 'pending-review.png'),
    fullPage: true,
  });
  console.log('✓ pending suggestions are confidence-ordered and truncation is explicit');

  await page
    .getByRole('button', { name: `Accept suggestion: ${description}`, exact: true })
    .click();
  const effectiveValues = page.locator('[aria-labelledby="effective-values-heading"]');
  await effectiveValues.getByText(description, { exact: true }).waitFor();
  await page.getByTestId(`annotation-${descriptionID}`).waitFor({ state: 'detached' });
  assert.equal(
    await effectiveValues.getByText(description, { exact: true }).count(),
    1,
    'accepted description must render once in the effective projection',
  );
  assert.equal(
    await page.locator('[aria-label="Raw AI observation (unconfirmed)"]').count(),
    0,
    'accepted description must not also render as an unconfirmed caption',
  );
  console.log('✓ accepting a description updates the effective projection');

  await page
    .getByRole('button', { name: `Reject suggestion: ${rejectedTag}`, exact: true })
    .click();
  await page.getByTestId(`annotation-${rejectTagID}`).waitFor({ state: 'detached' });
  assert.equal(await effectiveValues.getByText(rejectedTag, { exact: true }).count(), 0);

  const replay = await readJSON(
    page,
    `/v1/files/${fileID}/annotations/${rejectTagID}`,
    decisionOptions('rejected'),
  );
  assert.equal(replay.status, 200);
  assert.equal(replay.body.replayed, true);
  assert.equal(replay.body.annotation.state_version, 2);

  const opposite = await readJSON(
    page,
    `/v1/files/${fileID}/annotations/${rejectTagID}`,
    decisionOptions('accepted'),
  );
  assert.equal(opposite.status, 409);
  assert.equal(opposite.body.error, 'annotation_decision_conflict');
  console.log('✓ same-decision replay succeeds and opposite decision conflicts');

  const externalDecision = await readJSON(
    page,
    `/v1/files/${fileID}/annotations/${conflictTagID}`,
    decisionOptions('accepted'),
  );
  assert.equal(externalDecision.status, 200);
  assert.equal(externalDecision.body.replayed, false);

  await page
    .getByRole('button', { name: `Reject suggestion: ${conflictTag}`, exact: true })
    .click();
  await page.getByText('This suggestion was already reviewed elsewhere', { exact: true }).waitFor();
  await page.getByTestId(`annotation-${conflictTagID}`).waitFor({ state: 'detached' });
  await effectiveValues.getByText(conflictTag, { exact: true }).waitFor();
  console.log('✓ 409 conflict refetches current state and shows a visible toast');

  await page.goto(`${baseURL}/files/${rejectedDescriptionFileID}`, {
    waitUntil: 'domcontentloaded',
  });
  await page
    .getByRole('button', {
      name: `Reject suggestion: ${rejectedDescription}`,
      exact: true,
    })
    .click();
  await page.getByTestId(`annotation-${rejectedDescriptionID}`).waitFor({ state: 'detached' });
  const rejectedDescriptionDetail = await readJSON(page, `/v1/files/${rejectedDescriptionFileID}`);
  assert.equal(rejectedDescriptionDetail.status, 200);
  assert.equal(rejectedDescriptionDetail.body.summary, null);
  assert.equal(rejectedDescriptionDetail.body.caption, null);
  assert.equal(
    rejectedDescriptionDetail.body.annotations.find(
      (annotation) => annotation.id === rejectedDescriptionID,
    )?.status,
    'rejected',
  );
  assert.equal(await page.locator('[aria-label="Legacy AI summary (unreviewed)"]').count(), 0);
  assert.equal(await page.locator('[aria-label="Raw AI observation (unconfirmed)"]').count(), 0);
  console.log('✓ rejecting a legacy description removes every visible projection');

  await page.goto(`${baseURL}/drive`, { waitUntil: 'domcontentloaded' });
  await page.locator('input[type="file"]').setInputFiles({
    name: 'review-source.txt',
    mimeType: 'text/plain',
    buffer: Buffer.from('source metadata contract'),
  });
  await page.getByText(/^Uploaded 1 files to /).waitFor();
  await page.getByText('review-source.txt', { exact: true }).waitFor();
  const rootFiles = await readJSON(page, '/v1/files?path=%2F&limit=1000');
  const uploaded = rootFiles.body.files.find((file) => file.name === 'review-source.txt');
  assert.ok(uploaded, 'uploaded file should be visible in the root listing');
  assert.equal(uploaded.source_metadata.source_kind, 'web');

  const invalidSourceMetadata = await page.evaluate(async () => {
    const form = new FormData();
    form.append('file', new File(['bad metadata'], 'bad-source.txt', { type: 'text/plain' }));
    form.append('name', 'bad-source.txt');
    form.append('path', '/');
    form.append(
      'source_metadata',
      JSON.stringify({ source_kind: 'web', unexpected_device_id: 'secret' }),
    );
    const response = await fetch('/v1/files', { method: 'POST', body: form });
    return { status: response.status, body: await response.json() };
  });
  assert.equal(invalidSourceMetadata.status, 400);
  assert.equal(invalidSourceMetadata.body.error, 'bad_source_metadata');
  const nullSourceMetadataStatuses = await page.evaluate(async () => {
    const candidates = [
      { captured_at: null },
      { source_kind: null },
      { source_name: null },
      { location: null },
      { location: { lat: null, lon: 121 } },
      { location: { lat: 31, lon: null } },
      { location: { lat: 31, lon: 121, accuracy_m: null } },
      { location: { lat: 31, lon: 121, label: null } },
    ];
    return Promise.all(
      candidates.map(async (sourceMetadata, index) => {
        const form = new FormData();
        form.append(
          'file',
          new File(['null metadata'], `null-source-${index}.txt`, { type: 'text/plain' }),
        );
        form.append('name', `null-source-${index}.txt`);
        form.append('path', '/');
        form.append('source_metadata', JSON.stringify(sourceMetadata));
        const response = await fetch('/v1/files', { method: 'POST', body: form });
        return response.status;
      }),
    );
  });
  assert.deepEqual(nullSourceMetadataStatuses, Array(8).fill(400));
  const invisibleSourceMetadataStatuses = await page.evaluate(async () => {
    const candidates = [
      { source_name: 'phone\u200bsync' },
      { source_name: 'phone\u034fsync' },
      { location: { lat: 31, lon: 121, label: 'home\ufe0f' } },
    ];
    return Promise.all(
      candidates.map(async (sourceMetadata, index) => {
        const form = new FormData();
        form.append(
          'file',
          new File(['invisible metadata'], `invisible-source-${index}.txt`, {
            type: 'text/plain',
          }),
        );
        form.append('name', `invisible-source-${index}.txt`);
        form.append('path', '/');
        form.append('source_metadata', JSON.stringify(sourceMetadata));
        const response = await fetch('/v1/files', { method: 'POST', body: form });
        return response.status;
      }),
    );
  });
  assert.deepEqual(invisibleSourceMetadataStatuses, Array(3).fill(400));
  console.log('✓ browser upload and strict source metadata contract match the API');

  await page.goto(`${baseURL}/files/empty-enrichment-e2e`, {
    waitUntil: 'domcontentloaded',
  });
  await page.getByText('No suggestions awaiting review', { exact: true }).waitFor();
  assert.equal(await page.locator('[data-testid^="annotation-"]').count(), 0);
  const emptyDetail = await readJSON(page, '/v1/files/empty-enrichment-e2e');
  assert.equal(emptyDetail.status, 200);
  assert.equal(emptyDetail.body.index_status, 'done');
  assert.deepEqual(emptyDetail.body.annotations, []);
  console.log('✓ a completed empty enrichment is distinct from processing and failure');

  browserPhase = 'offline-detail';
  await page.goto(`${baseURL}/files/${offlineFileID}`, {
    waitUntil: 'domcontentloaded',
  });
  await page.getByText('Could not load file', { exact: true }).waitFor();
  await page
    .getByText('This is not an empty result. Check the network or service, then retry.', {
      exact: true,
    })
    .waitFor();
  assert.equal(await page.getByText('No suggestions awaiting review', { exact: true }).count(), 0);
  console.log('✓ an offline detail request is visible and never rendered as an empty result');

  const expectedOfflineRequestFailures = offlineRequestFailures.filter(
    (failure) => new URL(failure.url).pathname === offlineDetailAPIPath,
  );
  assert.ok(
    expectedOfflineRequestFailures.length > 0,
    `expected ${offlineDetailAPIPath} to fail while exercising the offline state`,
  );
  assert.deepEqual(
    offlineRequestFailures.filter(
      (failure) => new URL(failure.url).pathname !== offlineDetailAPIPath,
    ),
    [],
    `unexpected offline-phase request failures: ${JSON.stringify(offlineRequestFailures)}`,
  );
  const unexpectedConsoleErrors = consoleErrors.filter(
    ({ message, phase }) =>
      !message.includes('server responded with a status of 409 (Conflict)') &&
      !message.includes('server responded with a status of 400 (Bad Request)') &&
      !(
        phase === 'offline-detail' &&
        expectedOfflineRequestFailures.length > 0 &&
        message.includes('net::ERR_FAILED')
      ),
  );
  assert.deepEqual(pageErrors, [], `page errors: ${pageErrors.join('\n')}`);
  assert.deepEqual(
    unexpectedConsoleErrors,
    [],
    `console errors: ${unexpectedConsoleErrors
      .map(({ message, phase }) => `[${phase}] ${message}`)
      .join('\n')}`,
  );
  console.log(`✓ browser completed without errors; artifacts: ${artifactDir}`);
} catch (error) {
  failed = true;
  console.error(error);
} finally {
  if (browser) await browser.close().catch(() => {});
  await stopProcess(vite.child);
}

process.exit(failed ? 1 : 0);
