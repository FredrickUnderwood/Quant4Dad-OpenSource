import { useEffect, useState } from 'react';

type Theme = 'light' | 'dark' | 'auto';
const STORAGE_KEY = 'q4d-theme';

function readTheme(): Theme {
  const v = (typeof localStorage !== 'undefined' && localStorage.getItem(STORAGE_KEY)) || 'auto';
  return v === 'light' || v === 'dark' ? v : 'auto';
}

function applyTheme(t: Theme) {
  document.documentElement.setAttribute('data-theme', t);
  try { localStorage.setItem(STORAGE_KEY, t); } catch {}
}

export default function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>(readTheme);

  useEffect(() => { applyTheme(theme); }, [theme]);

  const Btn = ({ value, title, children }: { value: Theme; title: string; children: React.ReactNode }) => (
    <button
      type="button"
      title={title}
      aria-pressed={theme === value}
      aria-label={title}
      onClick={() => setTheme(value)}
    >
      {children}
    </button>
  );

  return (
    <div className="theme-toggle" role="group" aria-label="主题切换">
      <Btn value="light" title="浅色">
        {/* sun */}
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
          <circle cx="12" cy="12" r="4" />
          <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41" />
        </svg>
      </Btn>
      <Btn value="auto" title="跟随系统">
        {/* monitor */}
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
          <rect x="3" y="4" width="18" height="13" rx="2" />
          <path d="M8 21h8M12 17v4" />
        </svg>
      </Btn>
      <Btn value="dark" title="深色">
        {/* moon */}
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
          <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
        </svg>
      </Btn>
    </div>
  );
}
