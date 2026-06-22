/**
 * Minimal, dependency-free Markdown renderer for LLM answers.
 * Handles the subset qwen/most chat models emit: headings, bold, italic,
 * inline code, fenced code, bullet/numbered lists, blockquotes, and paragraphs.
 * Citation tags like [1] are highlighted. Not a full CommonMark parser — just
 * enough to render answers cleanly without pulling in react-markdown.
 */
import * as React from 'react';

/** Inline: **bold**, *italic* / _italic_, `code`, and [N] citations. */
function renderInline(
  text: string,
  keyBase: string,
  onCitation?: (n: number) => void,
): React.ReactNode[] {
  const out: React.ReactNode[] = [];
  // Order matters: code first (so ** inside code is literal), then bold, italic, citation.
  const re = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*]+\*|_[^_]+_)|(\[\d+\])/g;
  let last = 0;
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const tok = m[0];
    const k = `${keyBase}-${i++}`;
    if (m[1]) {
      out.push(
        <code key={k} className="rounded bg-bg-inset px-1 py-0.5 font-mono text-[0.85em] text-fg">
          {tok.slice(1, -1)}
        </code>,
      );
    } else if (m[2]) {
      out.push(
        <strong key={k} className="font-semibold text-fg">
          {tok.slice(2, -2)}
        </strong>,
      );
    } else if (m[3]) {
      out.push(
        <em key={k} className="italic">
          {tok.slice(1, -1)}
        </em>,
      );
    } else if (m[4]) {
      const n = parseInt(tok.slice(1, -1), 10);
      out.push(
        onCitation ? (
          <button
            key={k}
            type="button"
            onClick={() => onCitation(n)}
            className="mx-0.5 rounded bg-accent/15 px-1 font-medium text-accent hover:bg-accent/25 transition-colors"
            title="跳转到来源 / open source"
          >
            {tok}
          </button>
        ) : (
          <span key={k} className="text-accent font-medium">
            {tok}
          </span>
        ),
      );
    }
    last = m.index + tok.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

export function Markdown({
  children,
  className,
  onCitation,
}: {
  children: string;
  className?: string;
  onCitation?: (n: number) => void;
}) {
  const blocks = React.useMemo(() => parseBlocks(children ?? ''), [children]);
  const inline = (txt: string, key: string) => renderInline(txt, key, onCitation);
  return (
    <div className={className}>
      {blocks.map((b, i) => {
        if (b.type === 'code') {
          return (
            <pre
              key={i}
              className="my-2 overflow-x-auto rounded-md border border-border bg-bg-inset p-3 text-xs font-mono leading-relaxed"
            >
              <code>{b.content}</code>
            </pre>
          );
        }
        if (b.type === 'heading') {
          const Tag = (`h${Math.min(b.level ?? 3, 4)}` as 'h1');
          return (
            <Tag key={i} className="mt-3 mb-1 font-semibold text-fg">
              {inline(b.content, `h${i}`)}
            </Tag>
          );
        }
        if (b.type === 'quote') {
          return (
            <blockquote
              key={i}
              className="my-2 border-l-2 border-border-strong pl-3 text-fg-muted italic"
            >
              {inline(b.content, `q${i}`)}
            </blockquote>
          );
        }
        if (b.type === 'ul' || b.type === 'ol') {
          const List = b.type === 'ol' ? 'ol' : 'ul';
          return (
            <List
              key={i}
              className={`my-2 ml-5 space-y-1 ${b.type === 'ol' ? 'list-decimal' : 'list-disc'}`}
            >
              {(b.items ?? []).map((it, j) => (
                <li key={j} className="leading-relaxed">
                  {inline(it, `li${i}-${j}`)}
                </li>
              ))}
            </List>
          );
        }
        return (
          <p key={i} className="my-2 leading-relaxed first:mt-0 last:mb-0">
            {inline(b.content, `p${i}`)}
          </p>
        );
      })}
    </div>
  );
}

type Block =
  | { type: 'p' | 'heading' | 'quote' | 'code'; content: string; level?: number }
  | { type: 'ul' | 'ol'; items: string[]; content: string };

function parseBlocks(src: string): Block[] {
  const lines = src.replace(/\r\n/g, '\n').split('\n');
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i] ?? '';
    // Fenced code block
    if (line.trim().startsWith('```')) {
      const buf: string[] = [];
      i++;
      while (i < lines.length && !(lines[i] ?? '').trim().startsWith('```')) {
        buf.push(lines[i] ?? '');
        i++;
      }
      i++; // skip closing fence
      blocks.push({ type: 'code', content: buf.join('\n') });
      continue;
    }
    if (line.trim() === '') {
      i++;
      continue;
    }
    // Heading
    const h = line.match(/^(#{1,4})\s+(.*)$/);
    if (h) {
      blocks.push({ type: 'heading', level: h[1]!.length, content: h[2]! });
      i++;
      continue;
    }
    // Blockquote
    if (/^>\s?/.test(line)) {
      blocks.push({ type: 'quote', content: line.replace(/^>\s?/, '') });
      i++;
      continue;
    }
    // Lists (consume consecutive list lines)
    if (/^\s*[-*]\s+/.test(line) || /^\s*\d+[.)]\s+/.test(line)) {
      const ordered = /^\s*\d+[.)]\s+/.test(line);
      const items: string[] = [];
      while (
        i < lines.length &&
        (/^\s*[-*]\s+/.test(lines[i] ?? '') || /^\s*\d+[.)]\s+/.test(lines[i] ?? ''))
      ) {
        items.push((lines[i] ?? '').replace(/^\s*(?:[-*]|\d+[.)])\s+/, ''));
        i++;
      }
      blocks.push({ type: ordered ? 'ol' : 'ul', items, content: '' });
      continue;
    }
    // Paragraph: gather until blank line / block boundary
    const buf: string[] = [line];
    i++;
    while (
      i < lines.length &&
      (lines[i] ?? '').trim() !== '' &&
      !/^(#{1,4})\s+/.test(lines[i] ?? '') &&
      !/^>\s?/.test(lines[i] ?? '') &&
      !/^\s*[-*]\s+/.test(lines[i] ?? '') &&
      !/^\s*\d+[.)]\s+/.test(lines[i] ?? '') &&
      !(lines[i] ?? '').trim().startsWith('```')
    ) {
      buf.push(lines[i] ?? '');
      i++;
    }
    blocks.push({ type: 'p', content: buf.join('\n') });
  }
  return blocks;
}
