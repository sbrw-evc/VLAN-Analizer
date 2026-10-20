import { useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { Activity, Database, LoaderCircle, PlugZap, ShieldCheck } from 'lucide-react';
import { get, post, put } from '../../api/client';
import type { Connections } from '../../api/types';
import { ErrorNotice, Field, Loading, PageHead, PasswordInput, Switch } from '../../components/ui';
import { useT } from '../../i18n';
import { useLoad } from '../../lib/auth';
import { toast } from '../../lib/toast';

type Source = 'gatus' | 'kuma' | 'entra';
type Values = Record<string, string | number | boolean>;

function ConnectionCard({ source, icon, title, subtitle, initial, secretKey, secretSet, children }: { source: Source; icon: ReactNode; title: string; subtitle: string; initial: Values; secretKey: string; secretSet: boolean; children: (v: Values, set: (k: string, x: string | number | boolean) => void) => ReactNode }) {
  const t = useT();
  const [values, setValues] = useState<Values>({ ...initial, [secretKey]: '' });
  const [saved, setSaved] = useState(secretSet);
  const [busy, setBusy] = useState<'' | 'save' | 'test'>('');
  const [error, setError] = useState<unknown>(null);
  const [result, setResult] = useState('');
  const set = (k: string, x: string | number | boolean) => setValues((v) => ({ ...v, [k]: x }));
  const body = () => {
    const b: Values = { ...values };
    if (!b[secretKey]) delete b[secretKey];
    return b;
  };
  const run = async (kind: 'save' | 'test') => {
    setBusy(kind);
    setError(null);
    setResult('');
    try {
      if (kind === 'save') {
        const r = await put<Connections>(`/api/settings/connections/${source}`, body());
        setSaved(Boolean((r[source] as Record<string, unknown>)[`${secretKey}_set`]));
        setValues((v) => ({ ...v, [secretKey]: '' }));
        toast(t('{name} saved. Settings apply immediately.', { name: title }));
      } else {
        const r = await post<{ message?: string; sites?: number; warnings?: string[] }>(`/api/settings/connections/${source}/test`, body());
        setResult(r.message ? t(r.message) : t('Connection verified. {n} websites found.', { n: r.sites ?? 0 }) + (r.warnings?.length ? ` ${r.warnings.join(' · ')}` : ''));
      }
    } catch (e) {
      setError(e);
    } finally {
      setBusy('');
    }
  };
  return (
    <form
      className="card"
      data-testid={`${source}-form`}
      onSubmit={(e: FormEvent) => {
        e.preventDefault();
        run('save');
      }}
    >
      <div className="card-head">
        <div className="row" style={{ flexWrap: 'nowrap' }}>
          <span className="iface-icon">{icon}</span>
          <div>
            <h2>{title}</h2>
            <p className="muted small">{subtitle}</p>
          </div>
        </div>
        <span className={`pill ${values.enabled ? 'pill-ok' : ''}`}>{values.enabled ? t('Enabled') : t('Off')}</span>
      </div>
      <div className="card-body">
        <Switch name="enabled" checked={!!values.enabled} onChange={(v) => set('enabled', v)} label={t('Enabled')} />
        {children(values, set)}
        <Field label={t('Secret')} hint={saved ? t('Saved in OpenBao. Leave empty to keep it.') : t('Nothing saved yet.')}>
          <PasswordInput name={secretKey} value={String(values[secretKey] ?? '')} onChange={(v) => set(secretKey, v)} autoComplete="new-password" />
        </Field>
        <ErrorNotice error={error} />
        {result && (
          <div className="notice notice-success" role="status">
            {result}
          </div>
        )}
      </div>
      <div className="card-foot">
        <button type="button" className="btn" disabled={!!busy} onClick={() => run('test')}>
          {busy === 'test' ? <LoaderCircle size={16} className="spin" /> : <PlugZap size={16} />}
          {t('Test connection')}
        </button>
        <button type="submit" className="btn btn-primary" disabled={!!busy}>
          {busy === 'save' && <LoaderCircle size={16} className="spin" />}
          {t('Save')}
        </button>
      </div>
    </form>
  );
}

export function ConnectionsPage() {
  const t = useT();
  const { data, error } = useLoad(() => get<Connections>('/api/settings/connections'));
  useEffect(() => {
    document.title = `${t('Connections')} · VLAN Analizer`;
  }, [t]);
  if (error && !data) return <ErrorNotice error={error} />;
  if (!data) return <Loading />;
  const { token_set, ...gatus } = data.gatus;
  const { password_set, ...kuma } = data.kuma;
  const { client_secret_set, redirect_uri, ...entra } = data.entra;
  return (
    <>
      <PageHead icon={<PlugZap size={22} />} title={t('Connections')} subtitle={t('Website sources and single sign-on. Secrets are stored in OpenBao and never sent back to the browser.')} />
      <ConnectionCard source="gatus" icon={<Activity size={19} />} title="Gatus" subtitle={t('Imports endpoints from the Gatus API or a mounted YAML file.')} initial={gatus} secretKey="token" secretSet={token_set}>
        {(v, set) => (
          <div className="grid-2">
            <Field label={t('API server URL')}>
              <input className="input" name="url" value={String(v.url ?? '')} onChange={(e) => set('url', e.target.value)} placeholder="https://status.example.com" />
            </Field>
            <Field label={t('Mounted YAML file')} hint={t('An alternative to the API.')}>
              <input className="input" name="config_path" value={String(v.config_path ?? '')} onChange={(e) => set('config_path', e.target.value)} placeholder="/config/gatus.yaml" />
            </Field>
          </div>
        )}
      </ConnectionCard>
      <ConnectionCard source="kuma" icon={<Database size={19} />} title="Uptime Kuma" subtitle={t('Read-only import from the Kuma MySQL or MariaDB database.')} initial={kuma} secretKey="password" secretSet={password_set}>
        {(v, set) => (
          <>
            <div className="grid-3">
              <Field label={t('Database host')}>
                <input className="input" name="host" value={String(v.host ?? '')} onChange={(e) => set('host', e.target.value)} placeholder="kuma-db.example.internal" />
              </Field>
              <Field label={t('Port')}>
                <input className="input" name="port" type="number" min={1} max={65535} value={Number(v.port)} onChange={(e) => set('port', Number(e.target.value))} />
              </Field>
              <Field label={t('Database')}>
                <input className="input" name="database" value={String(v.database ?? '')} onChange={(e) => set('database', e.target.value)} />
              </Field>
              <Field label={t('User name')}>
                <input className="input" name="user" value={String(v.user ?? '')} onChange={(e) => set('user', e.target.value)} />
              </Field>
              <Field label={t('Mounted CA file')} hint={t('Optional.')}>
                <input className="input" name="ca" value={String(v.ca ?? '')} onChange={(e) => set('ca', e.target.value)} placeholder="/config/kuma-db-ca.pem" />
              </Field>
              <Field label={t('Stable instance ID')}>
                <input className="input" name="instance" value={String(v.instance ?? '')} onChange={(e) => set('instance', e.target.value)} />
              </Field>
            </div>
            <Switch name="tls" checked={!!v.tls} onChange={(x) => set('tls', x)} label={t('Require TLS and verify the host name')} />
          </>
        )}
      </ConnectionCard>
      <ConnectionCard source="entra" icon={<ShieldCheck size={19} />} title={t('Microsoft Entra ID')} subtitle={t('Sign-in with the VLAN.Admin and VLAN.Viewer application roles.')} initial={entra} secretKey="client_secret" secretSet={client_secret_set}>
        {(v, set) => (
          <>
            <div className="grid-2">
              <Field label={t('Tenant ID')}>
                <input className="input" name="tenant" value={String(v.tenant ?? '')} onChange={(e) => set('tenant', e.target.value)} maxLength={36} />
              </Field>
              <Field label={t('Application (client) ID')}>
                <input className="input" name="client_id" value={String(v.client_id ?? '')} onChange={(e) => set('client_id', e.target.value)} maxLength={36} />
              </Field>
            </div>
            <p className="hint">
              {t('Web redirect URI')}: <code>{redirect_uri}</code>
            </p>
          </>
        )}
      </ConnectionCard>
    </>
  );
}
