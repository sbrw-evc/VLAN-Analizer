import { useEffect, useState, type FormEvent } from 'react';
import { LoaderCircle, UserPlus, Users } from 'lucide-react';
import { put } from '../../api/client';
import { ErrorNotice, Field, PageHead, PasswordInput, Segmented, Switch } from '../../components/ui';
import { useT } from '../../i18n';
import { toast } from '../../lib/toast';

/** Local accounts live in OpenBao as Argon2id hashes; saving one replaces it and revokes its sessions. */
export function UsersPage() {
  const t = useT();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [role, setRole] = useState<'viewer' | 'admin'>('viewer');
  const [disabled, setDisabled] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    document.title = `${t('Users')} · VLAN Analizer`;
  }, [t]);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await put('/api/users', { username, password, role, disabled });
      toast(t('{name} saved. Existing sessions of this account were signed out.', { name: username }));
      setUsername('');
      setPassword('');
      setDisabled(false);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <PageHead icon={<Users size={22} />} title={t('Users')} subtitle={t('Local administrator and viewer accounts. Entra ID users get their role from the application roles.')} />
      <form className="card" onSubmit={submit} data-testid="user-form">
        <div className="card-head">
          <div>
            <h2>
              <UserPlus size={18} />
              {t('Create or update a local user')}
            </h2>
            <p className="muted small">{t('Saving an existing user name replaces its password and role. Disabling an account also needs a replacement password.')}</p>
          </div>
        </div>
        <div className="card-body">
          <div className="grid-2">
            <Field label={t('User name')}>
              <input className="input" name="username" value={username} onChange={(e) => setUsername(e.target.value)} pattern="[a-zA-Z0-9_.\-]{1,64}" required autoComplete="off" />
            </Field>
            <Field label={t('New password')} hint={t('At least 12 characters.')}>
              <PasswordInput name="password" value={password} onChange={setPassword} autoComplete="new-password" />
            </Field>
          </div>
          <Field label={t('Role')}>
            <Segmented
              label={t('Role')}
              value={role}
              onChange={setRole}
              options={[
                { value: 'viewer', label: t('Viewer') },
                { value: 'admin', label: t('Administrator') },
              ]}
            />
          </Field>
          <Switch checked={disabled} onChange={setDisabled} label={t('Disable the account')} hint={t('A disabled account cannot sign in; its sessions end immediately.')} />
          <ErrorNotice error={error} />
        </div>
        <div className="card-foot">
          <button type="submit" className="btn btn-primary" disabled={busy || !username || password.length < 12}>
            {busy && <LoaderCircle size={16} className="spin" />}
            {t('Save user')}
          </button>
        </div>
      </form>
      <div className="card card-pad">
        <dl className="kv">
          <dt>{t('Administrator')}</dt>
          <dd>{t('Manages websites, monitoring, connections, users and VLAN interfaces.')}</dd>
          <dt>{t('Viewer')}</dt>
          <dd>{t('Sees the availability matrix, diagnostics and the network inventory.')}</dd>
        </dl>
      </div>
    </>
  );
}
