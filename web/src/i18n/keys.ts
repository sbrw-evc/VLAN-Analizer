// Finds the translation keys of the app: literal t(...) calls in the sources, the messages of ApiError made in the
// browser, and the English messages of the server (fail(w, status, "message") in internal/app and the validation
// errors of internal/hostnet and the settings models), which describeError translates by text.
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';

export function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sourceFiles(path);
    return /\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name) ? [path] : [];
  });
}

/** Literal keys passed to t(…) (string or template literal without ${}). */
export function translationKeys(code: string): string[] {
  const keys: string[] = [];
  const re = /\bt\(\s*(?:'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)"|`([^`$]*)`)/g;
  for (const m of code.matchAll(re)) keys.push((m[1] ?? m[2] ?? m[3]).replace(/\\(.)/g, '$1'));
  for (const m of code.matchAll(/new ApiError\(\d+, '((?:[^'\\]|\\.)*)'/g)) keys.push(m[1]);
  return keys;
}

/** The server files whose literal messages reach the browser. */
export const SERVER_FILES = ['internal/app/http.go', 'internal/app/vlans.go', 'internal/app/model.go', 'internal/app/auth.go', 'internal/hostnet/spec.go', 'internal/hostnet/server.go', 'internal/hostnet/detect.go', 'internal/hostnet/manager.go'];

/** Whole-literal messages of the API errors (messages built with fmt are left in English). */
export function serverMessages(root: string): string[] {
  const out = new Set<string>();
  for (const f of SERVER_FILES) {
    const path = join(root, f);
    if (!existsSync(path)) continue;
    const code = readFileSync(path, 'utf8');
    for (const m of code.matchAll(/fail\(w, \d+, "((?:[^"\\]|\\.)*)"\)/g)) out.add(m[1].replace(/\\(.)/g, '$1'));
    for (const m of code.matchAll(/errors\.New\("((?:[^"\\]|\\.)*)"\)/g)) out.add(m[1].replace(/\\(.)/g, '$1'));
    for (const m of code.matchAll(/ValidationError\{"((?:[^"\\]|\\.)*)"\}/g)) out.add(m[1]);
    for (const m of code.matchAll(/"message": "([^"]+)"/g)) out.add(m[1]);
    for (const m of code.matchAll(/env\.Warnings, "([^"]+)"/g)) out.add(m[1]);
    for (const m of code.matchAll(/const Err\w+ = "([^"]+)"/g)) out.add(m[1]);
    for (const m of code.matchAll(/"([a-z][^"]+): " \+/g)) out.add(m[1]);
  }
  // Probe and worker diagnostics shown in the drawer and the warning banner.
  for (const f of ['internal/app/probes.go', 'internal/app/worker.go', 'internal/app/application.go']) {
    const path = join(root, f);
    if (!existsSync(path)) continue;
    const code = readFileSync(path, 'utf8');
    for (const m of code.matchAll(/"(?:\w+_)?error": "([^"]+)"/g)) out.add(m[1]);
    for (const m of code.matchAll(/\["(?:\w+_)?error"\] = "([^"]+)"/g)) out.add(m[1]);
    for (const m of code.matchAll(/workerError = "([^"]+)"/g)) out.add(m[1]);
    for (const m of code.matchAll(/return "([A-Z][^"]+\.)"/g)) out.add(m[1]);
  }
  return [...out];
}
