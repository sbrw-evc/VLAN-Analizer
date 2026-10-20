import { useEffect, useState, type FormEvent } from 'react';
import { Gauge, LoaderCircle, Plus, Trash2 } from 'lucide-react';
import { AnimatePresence, motion } from 'motion/react';
import { get, put } from '../../api/client';
import type { Dashboard, ScanConfig } from '../../api/types';
import { ErrorNotice, Field, Loading, PageHead, Switch } from '../../components/ui';
import { useT } from '../../i18n';
import { useLoad } from '../../lib/auth';
import { toast } from '../../lib/toast';

interface DnsRow {
  id: number;
  iface: string;
  servers: string;
}

let rowId = 1;

export function MonitoringPage() {
  const t = useT();
  const { data, error } = useLoad(() => Promise.all([get<ScanConfig>('/api/settings/monitoring'), get<Dashboard>('/api/dashboard')]));
  const [scan, setScan] = useState<ScanConfig | null>(null);
  const [dns, setDns] = useState<DnsRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [saveError, setSaveError] = useState<unknown>(null);
  useEffect(() => {
    document.title = `${t('Monitoring')} · VLAN Analizer`;
  }, [t]);
  useEffect(() => {
    if (!data) return;
    setScan(data[0]);
    setDns(Object.entries(data[0].dns_servers ?? {}).map(([iface, servers]) => ({ id: rowId++, iface, servers: servers.join(', ') })));
  }, [data]);
  if (error && !data) return <ErrorNotice error={error} />;
  if (!scan || !data) return <Loading />;
  const interfaces = [...new Set([...data[1].interfaces.map((i) => i.interface), ...scan.interfaces])].sort();
  const info = new Map(data[1].interfaces.map((i) => [i.interface, i]));
  const set = <K extends keyof ScanConfig>(k: K, v: ScanConfig[K]) => setScan((s) => (s ? { ...s, [k]: v } : s));
  const toggle = (name: string) => set('interfaces', scan.interfaces.includes(name) ? scan.interfaces.filter((n) => n !== name) : [...scan.interfaces, name]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setSaveError(null);
    try {
      const servers: Record<string, string[]> = {};
      for (const row of dns) {
        if (!row.iface) continue;
        servers[row.iface] = row.servers.split(/[\s,;]+/).filter(Boolean);
      }
      const saved = await put<ScanConfig>('/api/settings/monitoring', { ...scan, dns_servers: servers });
      setScan(saved);
      toast(t('Monitoring settings saved. Changes apply to the next cycle.'));
    } catch (err) {
      setSaveError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="stack" onSubmit={submit} data-testid="monitoring-form">
      <PageHead
        icon={<Gauge size={22} />}
        title={t('Monitoring')}
        subtitle={t('Which interfaces probe, how often and how strictly.')}
        actions={
          <button type="submit" className="btn btn-primary" disabled={busy}>
            {busy && <LoaderCircle size={16} className="spin" />}
            {t('Save')}
          </button>
        }
      />
      <ErrorNotice error={saveError} />
      <div className="card">
        <div className="card-head">
          <div>
            <h2>{t('Probe interfaces')}</h2>
            <p className="muted small">{t('Leave all unselected to use every eligible interface automatically. Docker and veth interfaces are never selected automatically.')}</p>
          </div>
          <span className="pill pill-accent">{scan.interfaces.length ? t('{n} selected', { n: scan.interfaces.length }) : t('Automatic')}</span>
        </div>
        <div className="card-body">
          <div className="select-list">
            {interfaces.map((name) => {
              const i = info.get(name);
              return (
                <label key={name}>
                  <input type="checkbox" name="interfaces" value={name} checked={scan.interfaces.includes(name)} onChange={() => toggle(name)} />
                  <span className="select-main">
                    <strong>{name}</strong>
                    {i?.vlan_id != null && <span className="vlan-tag">VLAN {i.vlan_id}</span>}
                    <span className="muted small">{i ? i.addresses.join(', ') || t('No IPv4') : t('Not present')}</span>
                  </span>
                  {i?.probeable && <span className="pill pill-ok">{t('Eligible')}</span>}
                </label>
              );
            })}
          </div>
        </div>
      </div>
      <div className="card">
        <div className="card-head">
          <div>
            <h2>{t('DNS servers by interface')}</h2>
            <p className="muted small">{t('Without an override the host IPv4 resolvers are used. Queries leave through the interface itself.')}</p>
          </div>
          <button type="button" className="btn btn-sm" onClick={() => setDns((d) => [...d, { id: rowId++, iface: '', servers: '' }])}>
            <Plus size={15} />
            {t('Add override')}
          </button>
        </div>
        <div className="card-body">
          {dns.length === 0 && <span className="hint">{t('No overrides.')}</span>}
          <AnimatePresence initial={false}>
            {dns.map((row) => (
              <motion.div key={row.id} className="dns-row" initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: 'auto' }} exit={{ opacity: 0, height: 0 }}>
                <select className="input" value={row.iface} aria-label={t('Interface')} onChange={(e) => setDns((d) => d.map((x) => (x.id === row.id ? { ...x, iface: e.target.value } : x)))}>
                  <option value="">{t('Interface')}</option>
                  {interfaces.map((n) => (
                    <option key={n} value={n}>
                      {n}
                    </option>
                  ))}
                </select>
                <input className="input" value={row.servers} placeholder="192.168.20.1, 192.168.20.2" aria-label={t('DNS servers')} onChange={(e) => setDns((d) => d.map((x) => (x.id === row.id ? { ...x, servers: e.target.value } : x)))} />
                <button type="button" className="icon-btn" aria-label={t('Remove')} onClick={() => setDns((d) => d.filter((x) => x.id !== row.id))}>
                  <Trash2 size={16} />
                </button>
              </motion.div>
            ))}
          </AnimatePresence>
        </div>
      </div>
      <div className="card">
        <div className="card-head">
          <h2>{t('Schedule and limits')}</h2>
        </div>
        <div className="card-body">
          <div className="grid-2">
            <Field label={t('Interval, seconds')} hint="30–86400">
              <input className="input" name="interval" type="number" min={30} max={86400} value={scan.interval} onChange={(e) => set('interval', Number(e.target.value))} required />
            </Field>
            <Field label={t('Parallel checks')} hint="1–32">
              <input className="input" name="concurrency" type="number" min={1} max={32} value={scan.concurrency} onChange={(e) => set('concurrency', Number(e.target.value))} required />
            </Field>
            <Field label={t('Timeout, seconds')} hint="5–120">
              <input className="input" name="timeout" type="number" min={5} max={120} value={scan.timeout} onChange={(e) => set('timeout', Number(e.target.value))} required />
            </Field>
            <Field label={t('Stale after, seconds')} hint={t('Older results are shown as unknown.')}>
              <input className="input" name="stale_after" type="number" min={60} max={604800} value={scan.stale_after} onChange={(e) => set('stale_after', Number(e.target.value))} required />
            </Field>
          </div>
          <Switch name="browser" checked={scan.browser} onChange={(v) => set('browser', v)} label={t('Check page loading with Chromium')} hint={t('Failed page resources mark a path as degraded. Browsers use much more memory than socket probes.')} />
        </div>
      </div>
    </form>
  );
}
