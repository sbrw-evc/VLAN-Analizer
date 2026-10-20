import { useState } from 'react';
import { ArrowLeft, ArrowRight, Check, Database, KeyRound, LoaderCircle, PlugZap, ShieldCheck, Sparkles, UserRound, Vault } from 'lucide-react';
import { AnimatePresence, motion, useReducedMotion } from 'motion/react';
import { post } from '../api/client';
import type { SetupStatus } from '../api/types';
import { Brand, ErrorNotice, Field, PasswordInput, Preferences, Switch } from '../components/ui';
import { useT } from '../i18n';

type BaoMode = 'managed' | 'token' | 'approle';

interface Form {
  public_url: string;
  bao_mode: BaoMode;
  bao_url: string;
  bao_mount: string;
  bao_token: string;
  bao_role_id: string;
  bao_secret_id: string;
  ch_managed: boolean;
  ch_url: string;
  ch_user: string;
  ch_database: string;
  ch_password: string;
  admin_username: string;
  admin_password: string;
  admin_confirm: string;
  entra_enabled: boolean;
  entra_tenant: string;
  entra_client: string;
  entra_secret: string;
}

const GUID = /^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$/;

/** The first-run wizard: application URL, OpenBao, ClickHouse, the administrator and optional Entra ID. */
export function SetupPage({ status }: { status: SetupStatus }) {
  const t = useT();
  const reduce = useReducedMotion();
  const [step, setStep] = useState(0);
  const [dir, setDir] = useState(1);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [message, setMessage] = useState('');
  const [f, setF] = useState<Form>({
    public_url: window.location.origin,
    bao_mode: status.managed_available ? 'managed' : 'token',
    bao_url: 'http://127.0.0.1:8200',
    bao_mount: 'secret',
    bao_token: '',
    bao_role_id: '',
    bao_secret_id: '',
    ch_managed: status.managed_available,
    ch_url: 'http://127.0.0.1:8123',
    ch_user: 'vlan',
    ch_database: 'default',
    ch_password: '',
    admin_username: 'admin',
    admin_password: '',
    admin_confirm: '',
    entra_enabled: false,
    entra_tenant: '',
    entra_client: '',
    entra_secret: '',
  });
  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((x) => ({ ...x, [k]: v }));
  const steps = [
    { label: t('Access'), icon: Sparkles },
    { label: 'OpenBao', icon: Vault },
    { label: 'ClickHouse', icon: Database },
    { label: t('Administrator'), icon: UserRound },
    { label: 'Entra ID', icon: ShieldCheck },
  ];
  const bao = () => ({ url: f.bao_url, mount: f.bao_mount, mode: f.bao_mode, token: f.bao_token, role_id: f.bao_role_id, secret_id: f.bao_secret_id });
  const clickhouse = () => ({ managed: f.ch_managed, url: f.ch_url, user: f.ch_user, database: f.ch_database, password: f.ch_password });

  const check = (): string | null => {
    if (step === 0 && !/^https?:\/\/[^/\s]+$/.test(f.public_url.replace(/\/$/, ''))) return t('Enter the address users open, for example http://server:8000');
    if (step === 1 && f.bao_mode !== 'managed') {
      if (!f.bao_url || !/^[a-zA-Z0-9_-]{1,64}$/.test(f.bao_mount)) return t('Enter the OpenBao address and KV mount.');
      if (f.bao_mode === 'token' ? !f.bao_token : !f.bao_role_id || !f.bao_secret_id) return t('Enter the OpenBao credentials.');
    }
    if (step === 2 && !f.ch_managed && (!f.ch_url || !f.ch_user || !f.ch_password || !/^[a-zA-Z0-9_]{1,64}$/.test(f.ch_database))) return t('Enter the ClickHouse address, user, database and password.');
    if (step === 3) {
      if (!/^[a-zA-Z0-9_.-]{1,64}$/.test(f.admin_username)) return t('The user name may contain letters, digits, dots, dashes and underscores.');
      if (f.admin_password.length < 12) return t('The password needs at least 12 characters.');
      if (f.admin_password !== f.admin_confirm) return t('Passwords do not match.');
    }
    if (step === 4 && f.entra_enabled && (!GUID.test(f.entra_tenant) || !GUID.test(f.entra_client) || !f.entra_secret)) return t('Enter all Entra ID connection fields or turn Entra ID off.');
    return null;
  };

  const act = async (fn: () => Promise<void>) => {
    if (busy) return;
    setBusy(true);
    setError(null);
    setMessage('');
    try {
      await fn();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const go = (to: number) => {
    setDir(to > step ? 1 : -1);
    setStep(to);
    setError(null);
    setMessage('');
  };
  const next = () =>
    act(async () => {
      const problem = check();
      if (problem) throw new Error(problem);
      if (step === 1) await post('/api/setup/test/openbao', bao());
      if (step === 2) await post('/api/setup/test/clickhouse', clickhouse());
      if (step < 4) return go(step + 1);
      setMessage(t('Verifying services and saving your installation…'));
      await post('/api/setup/complete', {
        public_url: f.public_url.replace(/\/$/, ''),
        bao: bao(),
        clickhouse: clickhouse(),
        admin_username: f.admin_username,
        admin_password: f.admin_password,
        entra: { enabled: f.entra_enabled, tenant: f.entra_tenant, client_id: f.entra_client, client_secret: f.entra_secret },
      });
      window.location.assign('/signin?installed=1');
    });
  const test = (path: string, body: unknown) =>
    act(async () => {
      await post(path, body);
      setMessage(t('Connection verified'));
    });

  const redirect = `${f.public_url.replace(/\/$/, '')}/auth/entra/callback`;
  const panels = [
    <div className="step-panel" key="0" data-step="0">
      <h2>{t('Welcome to VLAN Analizer')}</h2>
      <p className="muted">{t('Local OpenBao and ClickHouse services are prepared automatically. Create your administrator account to secure this installation.')}</p>
      <Field label={t('Application URL')} hint={t('Use the exact address users will open. The first completed setup creates the initial administrator and permanently closes this wizard.')}>
        <input className="input" name="public_url" type="url" value={f.public_url} onChange={(e) => set('public_url', e.target.value)} placeholder="http://server:8000" />
      </Field>
    </div>,
    <div className="step-panel" key="1" data-step="1">
      <h2>{t('Connect OpenBao')}</h2>
      <p className="muted">{t('Bundled OpenBao is initialized and unsealed automatically. You can also connect an existing external instance.')}</p>
      <div className="choice-grid" role="radiogroup" aria-label={t('Authentication')}>
        {(
          [
            ['managed', t('Managed'), t('Bundled with this installation'), status.managed_available],
            ['token', t('Operator token'), t('Provisions a scoped AppRole'), true],
            ['approle', 'AppRole', t('Use an existing RoleID and SecretID'), true],
          ] as const
        ).map(([mode, title, hint, enabled]) => (
          <button key={mode} type="button" role="radio" aria-checked={f.bao_mode === mode} disabled={!enabled} className={`choice ${f.bao_mode === mode ? 'selected' : ''}`} data-mode={mode} onClick={() => set('bao_mode', mode)}>
            <strong>{mode === 'managed' ? <Sparkles size={16} /> : <KeyRound size={16} />}{title}</strong>
            <small>{hint}</small>
          </button>
        ))}
      </div>
      {f.bao_mode !== 'managed' && (
        <motion.div className="stack" style={{ gap: 12 }} initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: 'auto' }}>
          <div className="grid-2">
            <Field label={t('Server URL')}>
              <input className="input" name="bao_url" value={f.bao_url} onChange={(e) => set('bao_url', e.target.value)} />
            </Field>
            <Field label={t('KV v2 mount')}>
              <input className="input" name="bao_mount" value={f.bao_mount} onChange={(e) => set('bao_mount', e.target.value)} />
            </Field>
          </div>
          {f.bao_mode === 'token' ? (
            <Field label={t('Provisioning token')} hint={t('Used only during setup to create a scoped AppRole. The operator token is never saved.')}>
              <PasswordInput name="bao_token" value={f.bao_token} onChange={(v) => set('bao_token', v)} autoComplete="off" />
            </Field>
          ) : (
            <div className="grid-2">
              <Field label="RoleID">
                <input className="input" name="bao_role_id" value={f.bao_role_id} onChange={(e) => set('bao_role_id', e.target.value)} autoComplete="off" />
              </Field>
              <Field label="SecretID">
                <PasswordInput name="bao_secret_id" value={f.bao_secret_id} onChange={(v) => set('bao_secret_id', v)} autoComplete="off" />
              </Field>
            </div>
          )}
        </motion.div>
      )}
      <div>
        <button type="button" className="btn btn-sm" disabled={busy} onClick={() => test('/api/setup/test/openbao', bao())}>
          <PlugZap size={15} />
          {t('Test connection')}
        </button>
      </div>
    </div>,
    <div className="step-panel" key="2" data-step="2">
      <h2>{t('Connect ClickHouse')}</h2>
      <p className="muted">{t('Bundled ClickHouse credentials are generated automatically. External databases are optional.')}</p>
      <Switch name="ch_managed" checked={f.ch_managed} disabled={!status.managed_available} onChange={(v) => set('ch_managed', v)} label={t('Use the managed ClickHouse')} hint={t('Measurements expire after 90 days, inventory after 30 days.')} />
      {!f.ch_managed && (
        <motion.div className="stack" style={{ gap: 12 }} initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: 'auto' }}>
          <Field label={t('HTTP(S) URL')}>
            <input className="input" name="ch_url" value={f.ch_url} onChange={(e) => set('ch_url', e.target.value)} />
          </Field>
          <div className="grid-2">
            <Field label={t('User name')}>
              <input className="input" name="ch_user" value={f.ch_user} onChange={(e) => set('ch_user', e.target.value)} />
            </Field>
            <Field label={t('Database')}>
              <input className="input" name="ch_database" value={f.ch_database} onChange={(e) => set('ch_database', e.target.value)} />
            </Field>
          </div>
          <Field label={t('Password')}>
            <PasswordInput name="ch_password" value={f.ch_password} onChange={(v) => set('ch_password', v)} autoComplete="off" />
          </Field>
        </motion.div>
      )}
      <div>
        <button type="button" className="btn btn-sm" disabled={busy} onClick={() => test('/api/setup/test/clickhouse', clickhouse())}>
          <PlugZap size={15} />
          {t('Test connection')}
        </button>
      </div>
    </div>,
    <div className="step-panel" key="3" data-step="3">
      <h2>{t('Create the administrator')}</h2>
      <p className="muted">{t('This is your local application account. Its password is stored as an Argon2id hash in OpenBao.')}</p>
      <Field label={t('User name')}>
        <input className="input" name="admin_username" value={f.admin_username} onChange={(e) => set('admin_username', e.target.value)} autoComplete="username" />
      </Field>
      <div className="grid-2">
        <Field label={t('Password')} hint={t('At least 12 characters.')}>
          <PasswordInput name="admin_password" value={f.admin_password} onChange={(v) => set('admin_password', v)} autoComplete="new-password" />
        </Field>
        <Field label={t('Repeat password')}>
          <PasswordInput name="admin_confirm" value={f.admin_confirm} onChange={(v) => set('admin_confirm', v)} autoComplete="new-password" />
        </Field>
      </div>
    </div>,
    <div className="step-panel" key="4" data-step="4">
      <h2>
        {t('Microsoft Entra ID')} <span className="pill">{t('Optional')}</span>
      </h2>
      <p className="muted">{t('You can skip this step and connect Entra ID later in Administration.')}</p>
      <Switch checked={f.entra_enabled} onChange={(v) => set('entra_enabled', v)} label={t('Enable Entra ID sign-in')} hint={t('Assign the VLAN.Admin or VLAN.Viewer application roles. Tenant membership alone grants no access.')} />
      {f.entra_enabled && (
        <motion.div className="stack" style={{ gap: 12 }} initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: 'auto' }}>
          <div className="grid-2">
            <Field label={t('Tenant ID')}>
              <input className="input" value={f.entra_tenant} onChange={(e) => set('entra_tenant', e.target.value)} maxLength={36} />
            </Field>
            <Field label={t('Application (client) ID')}>
              <input className="input" value={f.entra_client} onChange={(e) => set('entra_client', e.target.value)} maxLength={36} />
            </Field>
          </div>
          <Field label={t('Client secret')}>
            <PasswordInput value={f.entra_secret} onChange={(v) => set('entra_secret', v)} autoComplete="off" />
          </Field>
          <p className="hint">
            {t('Web redirect URI')}: <code>{redirect}</code>
          </p>
        </motion.div>
      )}
    </div>,
  ];

  return (
    <div className="center-page">
      <div className="card setup-card" data-testid="setup">
        <div className="signin-top">
          <Brand />
          <Preferences />
        </div>
        <div className="signin-title">
          <h1>{t('Set up your workspace')}</h1>
          <p className="muted">{t('Connect your services and create your administrator account.')}</p>
        </div>
        <div className="wizard-steps">
          {steps.map((s, i) => (
            <div key={i} className={`wizard-step ${i === step ? 'active' : ''} ${i < step ? 'done' : ''}`}>
              <span className="num">{i < step ? <Check /> : i + 1}</span>
              <span>{s.label}</span>
            </div>
          ))}
        </div>
        <div style={{ position: 'relative', overflow: 'hidden' }}>
          <AnimatePresence mode="wait" initial={false} custom={dir}>
            <motion.div
              key={step}
              custom={dir}
              initial={reduce ? { opacity: 0 } : { opacity: 0, x: 24 * dir }}
              animate={{ opacity: 1, x: 0 }}
              exit={reduce ? { opacity: 0 } : { opacity: 0, x: -24 * dir }}
              transition={{ duration: 0.2, ease: [0.22, 1, 0.36, 1] }}
            >
              {panels[step]}
            </motion.div>
          </AnimatePresence>
        </div>
        <ErrorNotice error={error} />
        {message && (
          <div className="notice notice-success" role="status" data-testid="setup-message">
            {message}
          </div>
        )}
        <div className="wizard-foot">
          {step > 0 && (
            <button type="button" className="btn btn-ghost" data-testid="setup-back" disabled={busy} onClick={() => go(step - 1)}>
              <ArrowLeft size={16} />
              {t('Back')}
            </button>
          )}
          <span className="spacer" />
          <span className="muted small">{t('Step {n} of {total}', { n: step + 1, total: steps.length })}</span>
          <button type="button" className="btn btn-primary" data-testid="setup-next" disabled={busy} onClick={next}>
            {busy && <LoaderCircle size={16} className="spin" />}
            {step === 4 ? t('Complete installation') : t('Continue')}
            {step < 4 && !busy && <ArrowRight size={16} />}
          </button>
        </div>
      </div>
    </div>
  );
}
