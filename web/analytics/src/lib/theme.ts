// Theme toggle — dark-mode-as-class on <html>, persisted in
// localStorage. The inline bootstrap in app.html handles the
// first-paint decision; this module owns runtime toggling.

import { writable, type Readable } from 'svelte/store';
import { browser } from '$app/environment';

export type Theme = 'light' | 'dark' | 'system';

const STORAGE_KEY = 'hula:theme';

function effectiveFromSaved(saved: string | null): 'light' | 'dark' {
  if (saved === 'dark') return 'dark';
  if (saved === 'light') return 'light';
  if (browser && window.matchMedia('(prefers-color-scheme: dark)').matches) return 'dark';
  return 'light';
}

const initial: Theme = browser
  ? ((localStorage.getItem(STORAGE_KEY) as Theme) ?? 'system')
  : 'system';

const internal = writable<Theme>(initial);

export const theme: Readable<Theme> = internal;

export function setTheme(next: Theme): void {
  internal.set(next);
  if (!browser) return;
  if (next === 'system') {
    localStorage.removeItem(STORAGE_KEY);
  } else {
    localStorage.setItem(STORAGE_KEY, next);
  }
  applyTheme(next);
}

function applyTheme(t: Theme): void {
  if (!browser) return;
  const effective = t === 'system' ? effectiveFromSaved(null) : t;
  document.documentElement.classList.toggle('dark', effective === 'dark');
}

/** Cycle helper for the toolbar icon button. */
export function toggleTheme(): void {
  internal.update((cur) => {
    const next: Theme = cur === 'light' ? 'dark' : cur === 'dark' ? 'system' : 'light';
    if (browser) {
      if (next === 'system') localStorage.removeItem(STORAGE_KEY);
      else localStorage.setItem(STORAGE_KEY, next);
      applyTheme(next);
    }
    return next;
  });
}
