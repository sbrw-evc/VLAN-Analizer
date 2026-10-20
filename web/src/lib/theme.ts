import { useSyncExternalStore, type MouseEvent } from 'react';
import { flushSync } from 'react-dom';
import { animate } from 'motion/react';

/** Light / dark theme as in the Umbrella apps: `data-theme` on <html>, kept per browser, revealed in a circle. */
export type Theme = 'light' | 'dark';
export type Origin = { x: number; y: number };

const KEY = 'vlan.theme';
const listeners = new Set<() => void>();

export function reducedMotion() {
  try {
    return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  } catch {
    return false;
  }
}

export function originOf(e: MouseEvent<HTMLElement>): Origin {
  const r = e.currentTarget.getBoundingClientRect();
  return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
}

function systemTheme(): Theme {
  try {
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  } catch {
    return 'light';
  }
}

function savedTheme(): Theme | null {
  try {
    const v = localStorage.getItem(KEY);
    return v === 'light' || v === 'dark' ? v : null;
  } catch {
    return null;
  }
}

let theme: Theme = typeof window === 'undefined' ? 'light' : (savedTheme() ?? systemTheme());

export function applyTheme(t: Theme) {
  if (typeof document === 'undefined') return;
  document.documentElement.dataset.theme = t;
  document.documentElement.style.colorScheme = t;
}

applyTheme(theme);

type ViewTransition = { ready: Promise<void>; finished: Promise<void> };

function reveal(apply: () => void, origin?: Origin) {
  const root = document.documentElement;
  const x = origin?.x ?? window.innerWidth - 40;
  const y = origin?.y ?? 32;
  root.style.setProperty('--reveal-x', `${x}px`);
  root.style.setProperty('--reveal-y', `${y}px`);
  root.style.setProperty('--reveal-r', '0px');
  root.classList.add('theme-reveal');
  const doc = document as Document & { startViewTransition?: (cb: () => void) => ViewTransition };
  if (typeof doc.startViewTransition !== 'function') {
    root.classList.remove('theme-reveal');
    apply();
    return;
  }
  const vt = doc.startViewTransition(apply);
  const radius = Math.hypot(Math.max(x, window.innerWidth - x), Math.max(y, window.innerHeight - y));
  vt.ready
    .then(() => animate(0, radius, { duration: 0.6, ease: [0.22, 1, 0.36, 1], onUpdate: (v) => root.style.setProperty('--reveal-r', `${v}px`) }))
    .catch(() => undefined);
  vt.finished.finally(() => root.classList.remove('theme-reveal'));
}

export function setTheme(next: Theme, origin?: Origin) {
  const apply = () => {
    flushSync(() => {
      theme = next;
      listeners.forEach((l) => l());
    });
    applyTheme(next);
  };
  if (next === theme || reducedMotion()) apply();
  else reveal(apply, origin);
  try {
    localStorage.setItem(KEY, next);
  } catch {
    /* lasts until reload */
  }
}

export function useTheme(): Theme {
  return useSyncExternalStore(
    (cb) => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    },
    () => theme,
    () => theme,
  );
}
