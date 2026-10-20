import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react';
import { get, post, setUnauthorizedHandler } from '../api/client';
import type { Session } from '../api/types';

interface AuthState {
  account: Session | null;
  loading: boolean;
  refresh: () => Promise<Session | null>;
  signOut: () => Promise<void>;
}

const Ctx = createContext<AuthState>({ account: null, loading: true, refresh: async () => null, signOut: async () => undefined });

export function AuthProvider({ children }: { children: ReactNode }) {
  const [account, setAccount] = useState<Session | null>(null);
  const [loading, setLoading] = useState(true);
  const refresh = useCallback(async () => {
    try {
      const a = await get<Session>('/api/me', { quiet401: true });
      setAccount(a);
      return a;
    } catch {
      setAccount(null);
      return null;
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    refresh();
    setUnauthorizedHandler(() => setAccount(null));
    return () => setUnauthorizedHandler(null);
  }, [refresh]);
  const signOut = useCallback(async () => {
    try {
      await post('/auth/logout');
    } finally {
      setAccount(null);
      window.location.assign('/signin?signed_out=1');
    }
  }, []);
  return <Ctx.Provider value={{ account, loading, refresh, signOut }}>{children}</Ctx.Provider>;
}

export const useAuth = () => useContext(Ctx);

export const isAdmin = (a: Session | null) => a?.role === 'admin';

/** Loads data for a page; `reload` fetches it again without clearing what is shown. */
export function useLoad<T>(fn: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  const reload = useCallback(async () => {
    setLoading(true);
    try {
      setData(await fn());
      setError(null);
    } catch (e) {
      setError(e);
    } finally {
      setLoading(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  useEffect(() => {
    reload();
  }, [reload]);
  return { data, setData, error, loading, reload };
}

/** Calls fn every `ms` while the tab is visible. */
export function usePoll(fn: () => void, ms: number) {
  useEffect(() => {
    const id = window.setInterval(() => {
      if (!document.hidden) fn();
    }, ms);
    return () => window.clearInterval(id);
  }, [fn, ms]);
}
