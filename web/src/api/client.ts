import { t } from '../i18n';

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

let onUnauthorized: (() => void) | null = null;
/** Called when an API call answers 401 (the session ended). */
export function setUnauthorizedHandler(fn: (() => void) | null) {
  onUnauthorized = fn;
}

/** JSON over fetch with the session cookie; the server answers errors as {"detail": "..."} in English. */
export async function api<T>(method: string, path: string, body?: unknown, opts: { quiet401?: boolean } = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  let res: Response;
  try {
    res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), credentials: 'same-origin' });
  } catch {
    throw new ApiError(0, 'VLAN Analizer does not answer. Check the connection');
  }
  const text = await res.text();
  let data: unknown = undefined;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    /* not JSON */
  }
  if (!res.ok) {
    if (res.status === 401 && !opts.quiet401 && onUnauthorized) onUnauthorized();
    const detail = (data as { detail?: unknown } | undefined)?.detail;
    throw new ApiError(res.status, typeof detail === 'string' ? detail : res.statusText || 'Something went wrong');
  }
  return data as T;
}

export const get = <T>(path: string, opts?: { quiet401?: boolean }) => api<T>('GET', path, undefined, opts);
export const post = <T>(path: string, body: unknown = undefined) => api<T>('POST', path, body);
export const put = <T>(path: string, body: unknown) => api<T>('PUT', path, body);
export const del = <T = void>(path: string) => api<T>('DELETE', path);

/** A human message for an error, in the current language (server messages are English and translated by key). */
export function describeError(err: unknown): string {
  if (err instanceof ApiError) {
    const [head, ...rest] = err.message.split(': ');
    if (rest.length && head !== err.message) return `${t(head)}: ${rest.join(': ')}`;
    return t(err.message);
  }
  if (err instanceof Error) return err.message || t('Something went wrong');
  return t('Something went wrong');
}
