import assert from 'node:assert/strict';
import { readdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';

const webRoot = path.dirname(fileURLToPath(import.meta.url));
const srcRoot = path.join(webRoot, 'src');
const i18nPath = path.join(srcRoot, 'i18n', 'index.tsx');
const visibleAttributes = new Set([
  'alt',
  'aria-label',
  'description',
  'hint',
  'label',
  'message',
  'placeholder',
  'title',
]);
const visibleObjectProperties = new Set(['description', 'hint', 'label', 'message', 'title']);

// Product identifiers, protocols, hashes and input examples are intentionally
// verbatim. Keep this list narrow and review every addition.
const verbatimAllowlist = new Map([
  ['src/components/layout/Logo.tsx', new Set(['mem'])],
  ['src/pages/CheckpointDetailPage.tsx', new Set(['SHA-256'])],
  ['src/pages/FileDetailPage.tsx', new Set(['MIME', 'SHA-256'])],
  ['src/components/memory/MemoryDetail.tsx', new Set(['SHA-256'])],
  ['src/pages/LoginPage.tsx', new Set(['you@example.com', '••••••••'])],
  ['src/pages/ProvidersPage.tsx', new Set(['vendor:model'])],
]);

function relative(file) {
  return path.relative(webRoot, file).split(path.sep).join('/');
}

function sourceKind(file) {
  return file.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
}

function humanText(value) {
  const text = value.replace(/\s+/g, ' ').trim();
  if (!text) return false;
  if (/[\u3400-\u9fff]/u.test(text)) return true;
  return /[A-Za-z]{2,}\s+[A-Za-z]{2,}/u.test(text);
}

function allowed(file, value) {
  return verbatimAllowlist.get(relative(file))?.has(value.trim()) ?? false;
}

function lineOf(source, node) {
  return source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1;
}

function literalValue(node) {
  if (ts.isStringLiteralLike(node)) return node.text;
  if (ts.isJsxText(node)) return node.text;
  return null;
}

function collectExpressionLiterals(node, out) {
  if (ts.isJsxElement(node) || ts.isJsxSelfClosingElement(node) || ts.isJsxFragment(node)) {
    return;
  }
  if (ts.isStringLiteralLike(node)) {
    out.push(node);
    return;
  }
  if (ts.isTemplateExpression(node)) {
    out.push(node.head);
    for (const span of node.templateSpans) out.push(span.literal);
    return;
  }
  ts.forEachChild(node, (child) => collectExpressionLiterals(child, out));
}

async function sourceFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const full = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === 'i18n' || entry.name === 'mocks') continue;
      files.push(...(await sourceFiles(full)));
    } else if (/\.(ts|tsx)$/u.test(entry.name) && !entry.name.endsWith('.d.ts')) {
      files.push(full);
    }
  }
  return files;
}

function auditVisibleLiterals(file, source, failures) {
  const uiModule = /^src\/(components|pages|routes)\//u.test(relative(file));
  const report = (node, value, context) => {
    const text = value.replace(/\s+/g, ' ').trim();
    if (!humanText(text) || allowed(file, text)) return;
    failures.push(`${relative(file)}:${lineOf(source, node)} ${context}: ${JSON.stringify(text)}`);
  };

  const visit = (node) => {
    if (ts.isJsxText(node)) {
      report(node, node.text, 'JSX text');
    }

    if (ts.isJsxAttribute(node) && visibleAttributes.has(node.name.text)) {
      if (node.initializer && ts.isStringLiteral(node.initializer)) {
        report(node.initializer, node.initializer.text, `${node.name.text} attribute`);
      } else if (
        node.initializer &&
        ts.isJsxExpression(node.initializer) &&
        node.initializer.expression
      ) {
        const literals = [];
        collectExpressionLiterals(node.initializer.expression, literals);
        for (const literal of literals) {
          const value = literalValue(literal);
          if (value !== null) report(literal, value, `${node.name.text} expression`);
        }
      }
    }

    if (
      ts.isJsxExpression(node) &&
      node.expression &&
      (ts.isJsxElement(node.parent) || ts.isJsxFragment(node.parent))
    ) {
      const literals = [];
      collectExpressionLiterals(node.expression, literals);
      for (const literal of literals) {
        const value = literalValue(literal);
        if (value !== null) report(literal, value, 'JSX expression');
      }
    }

    if (
      uiModule &&
      ts.isPropertyAssignment(node) &&
      ((ts.isIdentifier(node.name) && visibleObjectProperties.has(node.name.text)) ||
        (ts.isStringLiteral(node.name) && visibleObjectProperties.has(node.name.text)))
    ) {
      const value = literalValue(node.initializer);
      if (value !== null) report(node.initializer, value, `object ${node.name.getText(source)}`);
    }

    ts.forEachChild(node, visit);
  };

  visit(source);
}

function readDictionary(source) {
  let dictionary;
  const visit = (node) => {
    if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.name.text === 'dict') {
      const initializer = ts.isSatisfiesExpression(node.initializer)
        ? node.initializer.expression
        : node.initializer;
      if (initializer && ts.isObjectLiteralExpression(initializer)) dictionary = initializer;
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  assert.ok(dictionary, 'could not find the i18n dictionary');

  const entries = new Map();
  const failures = [];
  for (const property of dictionary.properties) {
    if (!ts.isPropertyAssignment(property)) continue;
    const key = ts.isStringLiteral(property.name)
      ? property.name.text
      : property.name.getText(source);
    if (!ts.isObjectLiteralExpression(property.initializer)) {
      failures.push(`i18n key ${key} must contain an inline zh/en object`);
      continue;
    }
    const translations = new Map();
    for (const translation of property.initializer.properties) {
      if (!ts.isPropertyAssignment(translation)) continue;
      const lang = translation.name.getText(source).replaceAll("'", '');
      const value = literalValue(translation.initializer);
      if (value !== null) translations.set(lang, value);
    }
    for (const lang of ['zh', 'en']) {
      if (!translations.get(lang)?.trim()) failures.push(`i18n key ${key} is missing ${lang}`);
    }
    const placeholders = (value) =>
      new Set(Array.from(value.matchAll(/\{(\w+)\}/gu), (match) => match[1]));
    if (translations.has('zh') && translations.has('en')) {
      assert.deepEqual(
        placeholders(translations.get('zh')),
        placeholders(translations.get('en')),
        `placeholder mismatch for ${key}`,
      );
    }
    entries.set(key, translations);
  }
  return { entries, failures };
}

function collectLiteralTranslationKeys(source, keys) {
  const visit = (node) => {
    if (
      ts.isCallExpression(node) &&
      ts.isIdentifier(node.expression) &&
      (node.expression.text === 't' || node.expression.text === 'tt') &&
      node.arguments.length > 0 &&
      ts.isStringLiteral(node.arguments[0])
    ) {
      keys.add(node.arguments[0].text);
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
}

const i18nSource = ts.createSourceFile(
  i18nPath,
  await readFile(i18nPath, 'utf8'),
  ts.ScriptTarget.Latest,
  true,
  ts.ScriptKind.TSX,
);
const { entries, failures } = readDictionary(i18nSource);
const usedKeys = new Set();

for (const file of await sourceFiles(srcRoot)) {
  const source = ts.createSourceFile(
    file,
    await readFile(file, 'utf8'),
    ts.ScriptTarget.Latest,
    true,
    sourceKind(file),
  );
  auditVisibleLiterals(file, source, failures);
  collectLiteralTranslationKeys(source, usedKeys);
}

for (const key of usedKeys) {
  if (!entries.has(key)) failures.push(`translation key ${key} is used but not defined`);
}

if (failures.length > 0) {
  console.error('Localization audit failed:\n' + failures.map((item) => `- ${item}`).join('\n'));
  process.exitCode = 1;
} else {
  console.log(
    `PASS: ${entries.size} bilingual keys have parity; ${usedKeys.size} literal usages resolve; no visible hard-coded prose`,
  );
}
