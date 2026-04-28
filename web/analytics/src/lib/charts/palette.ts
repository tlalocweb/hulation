// Color-blind-safe palette — Okabe-Ito (2008). Same list used across
// every chart component, so multi-series plots stay coherent.
//
// Order chosen so the first three colors contrast strongly against
// each other (blue / orange / green) — the UI's most common case is
// 2–3 series.

export const CHART_COLORS = [
  '#0072B2', // blue
  '#E69F00', // orange
  '#009E73', // bluish green
  '#CC79A7', // reddish purple
  '#56B4E9', // sky blue
  '#D55E00', // vermillion
  '#F0E442', // yellow
  '#000000', // black (fallback)
] as const;

export function colorFor(index: number): string {
  return CHART_COLORS[index % CHART_COLORS.length];
}

// Muted foreground for grid lines, axes, and background text. Reads
// the shadcn-svelte CSS variable so it flips with theme.
export const MUTED_FG = 'hsl(var(--muted-foreground))';
export const BORDER = 'hsl(var(--border))';
export const FG = 'hsl(var(--foreground))';
