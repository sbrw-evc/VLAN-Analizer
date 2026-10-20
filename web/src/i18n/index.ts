import { useSyncExternalStore } from 'react';
import { en } from './en';
import { ru } from './ru';

/**
 * English strings are the keys; each language maps every key to its text (en.ts and ru.ts, checked by
 * completeness.test.ts). `{name}` placeholders are interpolated.
 */
export type Lang = 'en' | 'ru';
export const LANGS: Array<{ code: Lang; label: string }> = [
  { code: 'en', label: 'English' },
  { code: 'ru', label: 'Русский' },
];

export const dictionaries: Record<Lang, Record<string, string>> = { en, ru };
const KEY = 'lang';

function initialLang(): Lang {
  try {
    const saved = localStorage.getItem(KEY);
    if (saved === 'en' || saved === 'ru') return saved;
  } catch {
    /* ignore */
  }
  return typeof navigator !== 'undefined' && navigator.language?.toLowerCase().startsWith('ru') ? 'ru' : 'en';
}

let lang: Lang = initialLang();
const listeners = new Set<() => void>();
if (typeof document !== 'undefined') document.documentElement.lang = lang;

export function getLang(): Lang {
  return lang;
}

export function setLang(next: Lang) {
  lang = next;
  try {
    localStorage.setItem(KEY, next);
  } catch {
    /* ignore */
  }
  if (typeof document !== 'undefined') document.documentElement.lang = next;
  listeners.forEach((l) => l());
}

export function translate(l: Lang, key: string, vars?: Record<string, string | number>): string {
  const s = dictionaries[l][key] ?? key;
  if (!vars) return s;
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
}

/** Translation in the current language outside components (toasts, helpers). */
export function t(key: string, vars?: Record<string, string | number>): string {
  return translate(lang, key, vars);
}

export type TFn = typeof t;

const subscribe = (cb: () => void) => {
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
  };
};

export function useLang(): Lang {
  return useSyncExternalStore(subscribe, () => lang, () => lang);
}

/** Subscribes the component to language changes and returns `t`. */
export function useT(): TFn {
  const current = useLang();
  return (key, vars) => translate(current, key, vars);
}

/** Dates and times in the current language. */
export function formatDate(v?: string | null, withTime = true): string {
  if (!v) return '—';
  const d = new Date(v);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return '—';
  return d.toLocaleString(lang === 'ru' ? 'ru-RU' : 'en-GB', withTime ? { dateStyle: 'medium', timeStyle: 'short' } : { dateStyle: 'medium' });
}

/** A time of day in the current language. */
export function formatTime(v?: string | null): string {
  if (!v) return '—';
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleTimeString(lang === 'ru' ? 'ru-RU' : 'en-GB', { hour: '2-digit', minute: '2-digit' });
}

/** "5 minutes ago" in the current language. */
export function formatAgo(v?: string | null): string {
  if (!v) return '—';
  const d = new Date(v).getTime();
  if (Number.isNaN(d)) return '—';
  const s = Math.round((d - Date.now()) / 1000);
  const rtf = new Intl.RelativeTimeFormat(lang === 'ru' ? 'ru' : 'en', { numeric: 'auto' });
  const abs = Math.abs(s);
  if (abs < 60) return rtf.format(s, 'second');
  if (abs < 3600) return rtf.format(Math.round(s / 60), 'minute');
  if (abs < 86400) return rtf.format(Math.round(s / 3600), 'hour');
  if (abs < 86400 * 30) return rtf.format(Math.round(s / 86400), 'day');
  return formatDate(v, false);
}
