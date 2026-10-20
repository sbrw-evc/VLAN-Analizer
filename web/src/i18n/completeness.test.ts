// @vitest-environment node
import { readFileSync } from 'node:fs';
import { relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { en } from './en';
import { serverMessages, sourceFiles, translationKeys } from './keys';
import { ru } from './ru';

const SRC = fileURLToPath(new URL('..', import.meta.url));
const ROOT = fileURLToPath(new URL('../../..', import.meta.url));
const files = sourceFiles(SRC).filter((f) => !relative(SRC, f).startsWith('i18n'));
const used = new Map<string, string>();
for (const f of files) for (const k of translationKeys(readFileSync(f, 'utf8'))) used.set(k, relative(SRC, f));
for (const k of serverMessages(ROOT)) if (!used.has(k)) used.set(k, 'backend');
const dicts = { en, ru };

describe('i18n completeness', () => {
  it('finds the keys of the app and the server messages', () => {
    expect(used.size).toBeGreaterThan(250);
    expect(used.get('Sign in')).toBeDefined();
    expect(used.get('invalid credentials or authentication unavailable')).toBeDefined();
    expect(used.get('VLAN ID must be between 1 and 4094')).toBeDefined();
  });

  for (const [lang, dict] of Object.entries(dicts)) {
    it(`every key used by the app has a ${lang} text`, () => {
      const missing = [...used].filter(([k]) => !(k in dict)).map(([k, f]) => `${f}: ${k}`);
      expect(missing).toEqual([]);
    });

    it(`${lang} has no empty texts and keeps the {placeholders} of the key`, () => {
      const vars = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join();
      const broken = Object.entries(dict).filter(([k, v]) => !v.trim() || vars(k) !== vars(v));
      expect(broken).toEqual([]);
    });
  }

  it('English and Russian have the same keys', () => {
    const onlyEn = Object.keys(en).filter((k) => !(k in ru));
    const onlyRu = Object.keys(ru).filter((k) => !(k in en));
    expect({ onlyEn, onlyRu }).toEqual({ onlyEn: [], onlyRu: [] });
  });

  it('English texts are the keys', () => {
    expect(Object.entries(en).filter(([k, v]) => k !== v)).toEqual([]);
  });

  it('the dictionaries hold no stale keys', () => {
    const stale = Object.keys(ru).filter((k) => !used.has(k));
    expect(stale).toEqual([]);
  });
});
