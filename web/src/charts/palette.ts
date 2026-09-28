export const defaultChartPalette = {
  ink: '#1b1812', muted: '#8a8170', hair: '#d9cfb8', rule: '#c8bd9f',
  surface: '#fbf7ee', up: '#0e5c3a', down: '#a02619', accent: '#0e5c3a',
  researchBlue: '#2563eb', researchOrange: '#b45309', researchPurple: '#9333ea', researchTeal: '#0e7490',
};
export type ChartPalette = typeof defaultChartPalette;

export function readChartPalette(): ChartPalette {
  const style = getComputedStyle(document.documentElement);
  return Object.fromEntries(Object.entries(defaultChartPalette).map(([key, fallback]) =>
    [key, style.getPropertyValue(`--${key.replace(/[A-Z]/g, c => '-' + c.toLowerCase())}`).trim() || fallback])) as ChartPalette;
}
