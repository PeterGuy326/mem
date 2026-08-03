import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright';

const webRoot = path.dirname(fileURLToPath(import.meta.url));
const port = Number(process.env.THEME_E2E_PORT ?? 5196);
const baseURL = `http://127.0.0.1:${port}`;
const artifactDir = path.join(tmpdir(), `mem-theme-acceptance-${process.pid}`);

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
      // The development server is still starting.
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

async function addMockSession(context, options = {}) {
  await context.addInitScript(
    ({ language, persistedTheme }) => {
      localStorage.setItem('mem.token', 'theme-e2e-token');
      localStorage.setItem(
        'mem.user',
        JSON.stringify({
          id: 'user-1',
          email: 'theme-e2e@mem.dev',
          created_at: '2026-01-01T00:00:00Z',
        }),
      );
      localStorage.setItem('mem.lang', language);
      if (persistedTheme) localStorage.setItem('mem.theme', persistedTheme);
      document.addEventListener(
        'readystatechange',
        () => {
          if (document.readyState === 'interactive') {
            window.__themeAtInteractive = document.documentElement.dataset.theme ?? null;
          }
        },
        { once: true },
      );
    },
    {
      language: options.language ?? 'en',
      persistedTheme: options.persistedTheme ?? null,
    },
  );
}

async function assertTheme(page, theme) {
  const expectedColor = theme === 'light' ? '#fafafc' : '#0a0b0f';
  await page.waitForFunction(
    (expected) =>
      document.documentElement.classList.contains(expected) &&
      document.documentElement.dataset.theme === expected,
    theme,
  );
  assert.equal(
    await page.locator('meta[name="theme-color"]').getAttribute('content'),
    expectedColor,
  );
  assert.equal(await page.locator('[data-toast-theme]').getAttribute('data-toast-theme'), theme);
}

const vite = startVite();
let browser;
let failure;

try {
  await waitForServer(vite);
  await mkdir(artifactDir, { recursive: true });
  browser = await launchBrowser();

  const defaultContext = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await addMockSession(defaultContext);
  const defaultPage = await defaultContext.newPage();
  const defaultErrors = [];
  defaultPage.on('pageerror', (error) => defaultErrors.push(error.message));

  await defaultPage.goto(`${baseURL}/drive`, { waitUntil: 'domcontentloaded' });
  const lightToggle = defaultPage.getByRole('button', { name: 'Switch to light theme' });
  await lightToggle.waitFor();
  await assertTheme(defaultPage, 'dark');
  assert.equal(
    await defaultPage.evaluate(() => localStorage.getItem('mem.theme')),
    null,
    'the dark default must not masquerade as an explicit user preference',
  );
  await defaultPage.screenshot({
    path: path.join(artifactDir, 'drive-dark.png'),
    fullPage: true,
  });
  console.log('✓ dark default and global theme control');

  await lightToggle.click();
  await assertTheme(defaultPage, 'light');
  assert.equal(await defaultPage.evaluate(() => localStorage.getItem('mem.theme')), 'light');
  assert.equal(
    (await defaultPage.evaluate(() => getComputedStyle(document.body).backgroundColor)).replaceAll(
      ' ',
      '',
    ),
    'rgb(250,250,252)',
  );
  await defaultPage.reload({ waitUntil: 'domcontentloaded' });
  await defaultPage.getByRole('button', { name: 'Switch to dark theme' }).waitFor();
  await assertTheme(defaultPage, 'light');
  assert.equal(await defaultPage.evaluate(() => window.__themeAtInteractive), 'light');
  console.log('✓ toggle persists and applies before React renders');

  assert.deepEqual(defaultErrors, [], `unexpected page errors: ${defaultErrors.join('\n')}`);
  await defaultContext.close();

  const memoryContext = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await addMockSession(memoryContext, { language: 'zh', persistedTheme: 'light' });
  const memoryPage = await memoryContext.newPage();
  const memoryErrors = [];
  memoryPage.on('pageerror', (error) => memoryErrors.push(error.message));

  await memoryPage.goto(`${baseURL}/memories`, { waitUntil: 'domcontentloaded' });
  await memoryPage.getByRole('heading', { name: '记忆账本' }).waitFor();
  await memoryPage.getByRole('button', { name: '切换到深色主题' }).waitFor();
  await assertTheme(memoryPage, 'light');
  assert.equal(await memoryPage.evaluate(() => window.__themeAtInteractive), 'light');
  await memoryPage.screenshot({
    path: path.join(artifactDir, 'memory-light.png'),
    fullPage: true,
  });
  console.log('✓ light memory ledger and Chinese accessible label');

  assert.deepEqual(memoryErrors, [], `unexpected page errors: ${memoryErrors.join('\n')}`);
  await memoryContext.close();
  console.log(`Theme acceptance passed. Screenshots: ${artifactDir}`);
} catch (error) {
  failure = error;
  process.exitCode = 1;
} finally {
  if (browser) await browser.close();
  await stopProcess(vite.child);
}

if (failure) throw failure;
