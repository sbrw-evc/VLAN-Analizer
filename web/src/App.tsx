import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { ServerCrash, ShieldOff } from 'lucide-react';
import { BrowserRouter, Link, Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { get } from './api/client';
import type { SetupStatus } from './api/types';
import { Shell } from './components/Shell';
import { Brand, Empty, Loading, Preferences } from './components/ui';
import { useT } from './i18n';
import { AuthProvider, isAdmin, useAuth } from './lib/auth';
import { Toasts } from './lib/toast';
import { NetworksPage } from './pages/Networks';
import { OverviewPage } from './pages/Overview';
import { SetupPage } from './pages/Setup';
import { SignInPage } from './pages/SignIn';
import { VlansPage } from './pages/Vlans';
import { ConnectionsPage } from './pages/settings/Connections';
import { IntegrationsPage } from './pages/settings/Integrations';
import { MonitoringPage } from './pages/settings/Monitoring';
import { UsersPage } from './pages/settings/Users';

/** Pages behind the sign-in; administration pages need the administrator role. */
function Private({ children, admin }: { children: ReactNode; admin?: boolean }) {
  const t = useT();
  const { account, loading } = useAuth();
  const location = useLocation();
  if (loading) return <Loading />;
  if (!account) return <Navigate to={`/signin?return=${encodeURIComponent(location.pathname + location.search)}`} replace />;
  return (
    <Shell>
      {admin && !isAdmin(account) ? (
        <div className="card">
          <Empty icon={<ShieldOff className="icon" />} title={t('Only administrators see this page')} action={<Link to="/">{t('Availability')}</Link>} />
        </div>
      ) : (
        children
      )}
    </Shell>
  );
}

function NotFound() {
  const t = useT();
  return (
    <div className="card">
      <Empty icon={<ShieldOff className="icon" />} title={t('Page not found')} action={<Link to="/">{t('Availability')}</Link>} />
    </div>
  );
}

/** Shown while the saved OpenBao and ClickHouse services cannot be reached. */
function Unavailable({ message, retry }: { message: string; retry: () => void }) {
  const t = useT();
  return (
    <div className="center-page">
      <div className="card signin-card" data-testid="unavailable">
        <div className="signin-top">
          <Brand />
          <Preferences />
        </div>
        <Empty icon={<ServerCrash className="icon" />} title={t('Workspace unavailable')} text={message ? t(message) : t('Waiting for configured services to become available.')} action={<button type="button" className="btn btn-primary" onClick={retry}>{t('Retry')}</button>} />
      </div>
    </div>
  );
}

/** Sends every page to the setup wizard until the installation is complete (GET /api/setup/status). */
function SetupGate({ children }: { children: (status: SetupStatus) => ReactNode }) {
  const location = useLocation();
  const [status, setStatus] = useState<SetupStatus | null>(null);
  const [error, setError] = useState('');
  const load = useCallback(() => {
    setError('');
    get<SetupStatus>('/api/setup/status')
      .then(setStatus)
      .catch(() => setError('VLAN Analizer does not answer. Check the connection'));
  }, []);
  useEffect(load, [load]);
  useEffect(() => {
    if (status && !status.required && !status.ready) {
      const id = window.setTimeout(load, 5000);
      return () => window.clearTimeout(id);
    }
  }, [status, load]);
  if (error) return <Unavailable message={error} retry={load} />;
  if (!status) return <Loading />;
  if (status.required) return location.pathname === '/setup' ? <SetupPage status={status} /> : <Navigate to="/setup" replace />;
  if (!status.ready) return <Unavailable message={status.error} retry={load} />;
  if (location.pathname === '/setup') return <Navigate to="/signin" replace />;
  return <>{children(status)}</>;
}

export function App() {
  return (
    <BrowserRouter>
      <SetupGate>
        {(status) => (
          <AuthProvider>
            <Routes>
              <Route path="/signin" element={<SignInPage entra={status.entra_enabled} />} />
              <Route path="/" element={<Private><OverviewPage /></Private>} />
              <Route path="/networks" element={<Private><NetworksPage /></Private>} />
              <Route path="/vlans" element={<Private admin><VlansPage /></Private>} />
              <Route path="/settings" element={<Navigate to="/settings/monitoring" replace />} />
              <Route path="/settings/monitoring" element={<Private admin><MonitoringPage /></Private>} />
              <Route path="/settings/connections" element={<Private admin><ConnectionsPage /></Private>} />
              <Route path="/settings/users" element={<Private admin><UsersPage /></Private>} />
              <Route path="/settings/integrations" element={<Private admin><IntegrationsPage /></Private>} />
              <Route path="*" element={<Private><NotFound /></Private>} />
            </Routes>
          </AuthProvider>
        )}
      </SetupGate>
      <Toasts />
    </BrowserRouter>
  );
}
