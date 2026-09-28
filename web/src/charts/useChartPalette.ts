import { useEffect, useState } from 'react';
import { readChartPalette } from './palette.ts';

/** Shared by research and backtests, including the system-theme setting. */
export function useChartPalette() {
  const [palette, setPalette] = useState(readChartPalette);
  useEffect(() => {
    const update = () => setPalette(readChartPalette());
    const observer = new MutationObserver(update);
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    const media = window.matchMedia('(prefers-color-scheme: dark)');
    media.addEventListener('change', update);
    update();
    return () => { observer.disconnect(); media.removeEventListener('change', update); };
  }, []);
  return palette;
}
