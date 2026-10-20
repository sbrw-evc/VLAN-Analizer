import { useEffect, useState, type FormEvent } from 'react';
import { CircleCheck, LoaderCircle, LogIn } from 'lucide-react';
import { motion } from 'motion/react';
import { Navigate, useSearchParams } from 'react-router-dom';
import { post } from '../api/client';
import { Brand, EntraMark, ErrorNotice, Field, PasswordInput, Preferences, spring } from '../components/ui';
import { useT } from '../i18n';
import { useAuth } from '../lib/auth';

export function SignInPage({ entra }: { entra: boolean }) {
  const t = useT();
  const { account, refresh } = useAuth();
  const [params] = useSearchParams();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const target = params.get('return');
  const back = target && target.startsWith('/') && !target.startsWith('//') ? target : '/';
  useEffect(() => {
    document.title = `${t('Sign in')} · VLAN Analizer`;
  }, [t]);
  if (account) return <Navigate to={back} replace />;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await post('/auth/local', { username, password });
      setPassword('');
      await refresh();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="center-page">
      <motion.div className="card signin-card" data-testid="signin" initial={{ opacity: 0, y: 12, scale: 0.98 }} animate={{ opacity: 1, y: 0, scale: 1 }} transition={spring}>
        <div className="signin-top">
          <Brand />
          <Preferences />
        </div>
        <div className="signin-title">
          <h1>{t('Every network. Every destination.')}</h1>
          <p className="muted">{t('Sign in to inspect availability across your VLANs.')}</p>
        </div>
        {params.get('installed') && (
          <div className="notice notice-success" style={{ marginTop: 16 }}>
            <CircleCheck size={16} className="icon" /> {t('Installation complete. Sign in with the administrator account.')}
          </div>
        )}
        <form className="signin-form" onSubmit={submit} data-testid="signin-form">
          {entra && (
            <>
              <a className="btn btn-block" href="/auth/entra">
                <EntraMark />
                {t('Continue with Microsoft Entra ID')}
              </a>
              <div className="hint" style={{ textAlign: 'center' }}>
                {t('or use a local account')}
              </div>
            </>
          )}
          <Field label={t('User name')}>
            <input className="input" name="username" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoFocus maxLength={64} required />
          </Field>
          <Field label={t('Password')}>
            <PasswordInput name="password" value={password} onChange={setPassword} autoComplete="current-password" />
          </Field>
          <ErrorNotice error={error} />
          <button type="submit" className="btn btn-primary btn-block" disabled={busy || !username || !password}>
            {busy ? <LoaderCircle size={16} className="spin" /> : <LogIn size={16} />}
            {t('Sign in')}
          </button>
        </form>
        <p className="signin-foot" style={{ marginTop: 18 }}>
          {t('Credentials are protected by OpenBao')}
        </p>
      </motion.div>
    </div>
  );
}
