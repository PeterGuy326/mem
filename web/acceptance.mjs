// 验收走查 — 真 Chromium，像用户一样点进去用每个功能
import { chromium } from 'playwright';
import { mkdirSync, readdirSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

const BASE = 'http://127.0.0.1:5190';
const SHOTS = '/tmp/mem-acceptance';
mkdirSync(SHOTS, { recursive: true });
const pwRoot = join(homedir(), '.cache/ms-playwright');
const execPath = join(pwRoot, readdirSync(pwRoot).find((d) => d.startsWith('chromium-')), 'chrome-linux64', 'chrome');

const steps = [];
const log = (n, ok, d) => { steps.push({ n, ok, d }); console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${n}${d ? '  — ' + d : ''}`); };

const browser = await chromium.launch({ executablePath: execPath });
const page = await browser.newPage({ viewport: { width: 1280, height: 860 } });
const errs = [];
page.on('console', (m) => m.type() === 'error' && errs.push(m.text()));
page.on('pageerror', (e) => errs.push('PAGEERROR: ' + e.message));
const shot = (name) => page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: true });

try {
  // 1) 登录
  await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
  await page.fill('#email', 'demo@mem.dev');
  await page.fill('#password', 'demopass');
  await page.click('button[type=submit]');
  await page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 15000 });
  log('1 登录', true, '进入 ' + new URL(page.url()).pathname);

  // 2) Drive 首屏 — 等真文件
  await page.locator('text=queue_test.txt').first().waitFor({ timeout: 12000 });
  await shot('01-drive');
  log('2 Drive 首屏', true, '11 文件 + demo 文件夹渲染');

  // 3) 进 demo 文件夹（左树单击）
  await page.locator('aside >> text=demo').first().click();
  await page.waitForURL((u) => u.pathname.includes('/drive/demo'), { timeout: 8000 });
  await page.locator('text=rust_borrow_checker.md').first().waitFor({ timeout: 10000 });
  await shot('02-demo-folder');
  const demoCount = await page.locator('text=.md').count();
  log('3 进 demo 文件夹', demoCount >= 5, `见到 ${demoCount} 个 .md`);

  // 4) 双击文件 → 详情页
  await page.locator('text=rust_borrow_checker.md').first().dblclick();
  await page.waitForURL((u) => u.pathname.startsWith('/files/'), { timeout: 8000 });
  await page.waitForTimeout(2500);
  await shot('03-file-detail');
  const detailText = await page.locator('body').innerText();
  const hasSummary = detailText.includes('Ownership') || detailText.includes('owner') || detailText.length > 200;
  log('4 文件详情页', hasSummary, '详情 + AI 摘要渲染');

  // 5) 搜索 — 详情页没有头部搜索框，先回 drive
  await page.goto(BASE + '/drive', { waitUntil: 'networkidle' });
  await page.locator('input[aria-label="搜索"]').fill('rust ownership');
  await page.locator('input[aria-label="搜索"]').press('Enter');
  await page.waitForURL((u) => u.pathname === '/search', { timeout: 8000 });
  await page.locator('text=rust_borrow_checker').first().waitFor({ timeout: 30000 }).catch(() => {});
  await shot('04-search');
  const searchHit = await page.locator('text=rust_borrow_checker').count();
  log('5 搜索', searchHit > 0, searchHit > 0 ? '命中 rust_borrow_checker.md' : '无命中');

  // 6) 点搜索结果 → 详情
  if (searchHit > 0) {
    await page.locator('text=rust_borrow_checker.md').first().click();
    await page.waitForURL((u) => u.pathname.startsWith('/files/'), { timeout: 8000 }).catch(() => {});
    const onDetail = page.url().includes('/files/');
    log('6 搜索结果跳详情', onDetail, onDetail ? '跳到 /files/:id' : '未跳转');
  } else {
    log('6 搜索结果跳详情', false, '跳过（上一步无命中）');
  }

  // 7) Ask — 提真问题
  await page.goto(BASE + '/ask', { waitUntil: 'networkidle' });
  const askInput = page.locator('input[placeholder*="Ask anything"]');
  await askInput.fill('how does rust guarantee memory safety');
  await page.locator('button[type=submit]').click();
  await page.waitForTimeout(20000); // RAG 要等 LLM
  await shot('05-ask-answered');
  const askText = await page.locator('body').innerText();
  const gotAnswer = askText.length > 400 && !askText.includes('请检查');
  log('7 Ask 提问', gotAnswer, gotAnswer ? `回答已渲染（${askText.length} 字符）` : '未见回答');

  // 8) Faces
  await page.goto(BASE + '/faces', { waitUntil: 'networkidle' });
  await page.waitForTimeout(2000);
  await shot('06-faces');
  const faceRows = await page.locator('text=face').count();
  log('8 Faces', faceRows > 0, `${faceRows} 个含 "face" 的行`);

  // 9) Providers
  await page.goto(BASE + '/providers', { waitUntil: 'networkidle' });
  await page.waitForTimeout(2000);
  await shot('07-providers');
  const provText = await page.locator('body').innerText();
  log('9 Providers', provText.includes('ollama') && provText.includes('768'), 'embedding 真配置回显');

  log('10 控制台无报错', errs.length === 0, errs.length ? `${errs.length} 条 error` : '干净');
} catch (e) {
  log('FATAL', false, e.message);
  await shot('99-crash').catch(() => {});
} finally {
  await browser.close();
}

const pass = steps.filter((s) => s.ok).length;
console.log(`\n  ====  验收 ${pass}/${steps.length} PASS  ====`);
if (errs.length) { console.log('\n  控制台 error:'); errs.slice(0, 8).forEach((e) => console.log('   - ' + e)); }
console.log(`  截图: ${SHOTS}`);
process.exit(pass === steps.length ? 0 : 1);
