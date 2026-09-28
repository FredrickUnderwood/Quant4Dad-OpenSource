import { memo, useRef, useState, type ComponentPropsWithoutRef } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

function CodeBlock({ children }: ComponentPropsWithoutRef<'pre'>) {
  const code = useRef<HTMLPreElement>(null);
  const [copied, setCopied] = useState(false), [error, setError] = useState(false);
  async function copy() {
    try { await navigator.clipboard.writeText(code.current?.textContent ?? ''); setCopied(true); setError(false); }
    catch { setError(true); }
  }
  return <div className="assistant-code-block"><div className="assistant-code-toolbar"><span>代码</span><button type="button" className="ghost" onClick={copy}>{error ? '复制失败，请手动复制' : copied ? '已复制' : '复制代码'}</button></div><pre ref={code}>{children}</pre></div>;
}

const components = {
  pre: CodeBlock,
  table: ({ children }: ComponentPropsWithoutRef<'table'>) => <div className="assistant-table-scroll" tabIndex={0} role="region" aria-label="表格"><table>{children}</table></div>,
  a: ({ href, children }: ComponentPropsWithoutRef<'a'>) => href ? <a href={href} target="_blank" rel="noopener noreferrer">{children}</a> : <span>{children}</span>,
  img: ({ src, alt }: ComponentPropsWithoutRef<'img'>) => <a href={src} target="_blank" rel="noopener noreferrer">{alt || '查看图片'}</a>,
};
const plugins = [remarkGfm];

// Raw HTML stays escaped; links use react-markdown's safe default URL transform.
export const Markdown = memo(function Markdown({ text }: { text: string }) {
  return <div className="assistant-markdown"><ReactMarkdown remarkPlugins={plugins} components={components}>{text}</ReactMarkdown></div>;
});
