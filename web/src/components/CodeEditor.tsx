import { useEffect, useState } from 'react';
import CodeMirror from '@uiw/react-codemirror';
import { python } from '@codemirror/lang-python';

// Resolve the active light/dark mode from <html data-theme>, resolving 'auto'
// via the OS preference — mirrors how charts read the palette elsewhere.
function resolveMode(): 'light' | 'dark' {
  const attr = document.documentElement.getAttribute('data-theme');
  if (attr === 'light' || attr === 'dark') return attr;
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function useThemeMode(): 'light' | 'dark' {
  const [mode, setMode] = useState<'light' | 'dark'>(resolveMode);
  useEffect(() => {
    const update = () => setMode(resolveMode());
    const mo = new MutationObserver(update);
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    mq.addEventListener?.('change', update);
    return () => { mo.disconnect(); mq.removeEventListener?.('change', update); };
  }, []);
  return mode;
}

interface Props {
  value: string;
  onChange: (code: string) => void;
  height?: number;
  readOnly?: boolean;
}

// CodeEditor wraps CodeMirror 6 with Starlark (Python) highlighting. Starlark is
// a Python subset, so the Python grammar is a good fit.
export default function CodeEditor({ value, onChange, height = 420, readOnly }: Props) {
  const mode = useThemeMode();
  return (
    <div style={{ border: '1px solid var(--rule)', borderRadius: 6, overflow: 'hidden' }}>
      <CodeMirror
        value={value}
        height={`${height}px`}
        theme={mode}
        readOnly={readOnly}
        extensions={[python()]}
        basicSetup={{ lineNumbers: true, foldGutter: true, highlightActiveLine: true }}
        onChange={onChange}
      />
    </div>
  );
}
