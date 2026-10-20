import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react';
import { Activity, CircleAlert, CircleCheck, CircleX, Globe, LoaderCircle, Network, Pencil, Play, Plus, RefreshCw, Search, Trash2, TriangleAlert } from 'lucide-react';
import { AnimatePresence, motion } from 'motion/react';
import { del, get, post, put } from '../api/client';
import type { CheckResult, Dashboard, HostInterface, Site, Status } from '../api/types';
import { HistoryChart } from '../components/HistoryChart';
import { Confirm, Drawer, Empty, ErrorNotice, Field, Modal, PageHead, Segmented, StatusChip, Switch, Tile } from '../components/ui';
import { formatAgo, formatDate, useT } from '../i18n';
import { isAdmin, useAuth, useLoad, usePoll } from '../lib/auth';
import { toast, toastError } from '../lib/toast';

const active = (s: Site) => !!s.enabled && s.source_enabled !== 0;
const vlanLabel = (t: (k: string, v?: Record<string, string | number>) => string, i: { vlan_id: number | null }) => (i.vlan_id == null ? t('Untagged') : t('VLAN {id}', { id: i.vlan_id }));
const ms = (v?: number | null) => (v == null ? '—' : `${v < 10 ? v.toFixed(1) : Math.round(v)}`);

function SourceMark({ source }: { source: Site['source'] }) {
  return (
    <span className={`source-mark source-${source}`} title={source}>
      {source === 'manual' ? <Globe size={16} /> : <Activity size={16} />}
    </span>
  );
}

function SiteDialog({ site, onClose, onSaved }: { site: Site | null; onClose: () => void; onSaved: () => void }) {
  const t = useT();
  const [name, setName] = useState(site?.name ?? '');
  const [url, setUrl] = useState(site?.url ?? '');
  const [enabled, setEnabled] = useState(site ? !!site.enabled : true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [confirm, setConfirm] = useState(false);
  const imported = !!site && site.source !== 'manual';
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (site) await put(`/api/sites/${encodeURIComponent(site.id)}`, { name, url, enabled });
      else await post('/api/sites', { name, url, enabled });
      toast(site ? t('Website saved') : t('Website added. The next check cycle starts now.'));
      onSaved();
      onClose();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      title={site ? t('Edit website') : t('Add website')}
      onClose={onClose}
      testId="site-dialog"
      footer={
        <>
          {site && !imported && (
            <button type="button" className="btn btn-danger-soft" onClick={() => setConfirm(true)}>
              <Trash2 size={16} />
              {t('Delete')}
            </button>
          )}
          <span className="spacer" />
          <button type="button" className="btn" onClick={onClose}>
            {t('Cancel')}
          </button>
          <button type="submit" form="site-form" className="btn btn-primary" disabled={busy}>
            {busy && <LoaderCircle size={16} className="spin" />}
            {t('Save website')}
          </button>
        </>
      }
    >
      <form id="site-form" className="stack" style={{ gap: 14 }} onSubmit={submit}>
        <Field label={t('Name')}>
          <input className="input" name="name" value={name} onChange={(e) => setName(e.target.value)} maxLength={120} required placeholder={t('Customer portal')} autoFocus />
        </Field>
        <Field label={t('Website URL')} hint={imported ? t('Imported URLs change in their source.') : t('HTTP(S) on ports 80, 443, 8080 or 8443. Redirects and page resources are checked through each interface.')}>
          <input className="input" name="url" type="url" value={url} onChange={(e) => setUrl(e.target.value)} maxLength={2048} required readOnly={imported} placeholder="https://portal.example.com" />
        </Field>
        <Switch name="enabled" checked={enabled} onChange={setEnabled} label={t('Monitoring enabled')} hint={imported ? t('Sites paused in their source stay paused until enabled there.') : undefined} />
        <ErrorNotice error={error} />
      </form>
      <AnimatePresence>
        {confirm && site && (
          <Confirm
            title={t('Delete website')}
            text={t('Delete {name}? Historical measurements are kept.', { name: site.name })}
            confirm={t('Delete')}
            danger
            onClose={() => setConfirm(false)}
            onConfirm={async () => {
              await del(`/api/sites/${encodeURIComponent(site.id)}`);
              toast(t('Website deleted'));
              onSaved();
              onClose();
            }}
          />
        )}
      </AnimatePresence>
    </Modal>
  );
}

function Stage({ label, value, unit, good, icon }: { label: string; value: string; unit?: string; good?: boolean | null; icon?: boolean }) {
  const t = useT();
  return (
    <div className={`stage ${good === false ? 'bad' : good ? 'good' : ''}`}>
      <span className="stage-label">
        {icon && (good === false ? <CircleX size={13} /> : good ? <CircleCheck size={13} /> : null)}
        {label}
      </span>
      <span className="stage-value">
        {value}
        {unit && value !== '—' && <span className="muted small"> {t(unit)}</span>}
      </span>
    </div>
  );
}

function DetailDrawer({ site, iface, result, onClose }: { site: Site; iface: HostInterface; result?: CheckResult; onClose: () => void }) {
  const t = useT();
  const { data: history, error } = useLoad(() => get<CheckResult[]>(`/api/history?${new URLSearchParams({ site_id: site.id, interface: iface.interface, hours: '24' })}`), [site.id, iface.interface]);
  const status: Status = result?.status ?? 'unknown';
  const r = result;
  const healthy = history?.filter((h) => h.status === 'up').length ?? 0;
  return (
    <Drawer
      onClose={onClose}
      testId="detail"
      header={
        <>
          <div className="row">
            <StatusChip status={active(site) ? status : 'paused'} />
            <span className="pill">{iface.interface}</span>
            <span className="pill pill-accent">{vlanLabel(t, iface)}</span>
          </div>
          <h2>{site.name}</h2>
          <span className="muted small">{site.url}</span>
        </>
      }
    >
      {!r && <div className="notice">{t('No fresh measurement yet. Results appear after the next check cycle.')}</div>}
      {r && (
        <>
          {r.measured_status && r.measured_status !== r.status && (
            <div className="notice notice-warning">
              {t('The last measurement is older than the freshness limit ({age}), so the path is shown as unknown.', { age: formatAgo(r.at) })}
            </div>
          )}
          <div className="stage-grid stagger">
            <Stage label="DNS" value={ms(r.dns?.ms)} unit="ms" good={r.dns ? true : r.errors?.some((e) => e.stage === 'dns') ? false : null} icon />
            <Stage label="TCP" value={ms(r.tcp_ms)} unit="ms" good={r.tcp_ms != null ? true : r.tcp_error ? false : null} icon />
            {r.tls && <Stage label="TLS" value={ms(r.tls.handshake_ms)} unit="ms" good={r.tls.valid} icon />}
            <Stage label="HTTP" value={r.http ? String(r.http.code) : '—'} good={r.http ? r.http.code < 400 : r.http_error ? false : null} icon />
            <Stage label={t('First byte')} value={ms(r.http?.ttfb_ms)} unit="ms" />
            <Stage label={t('Total')} value={ms(r.http?.total_ms)} unit="ms" />
            {r.browser && <Stage label={t('Page load')} value={ms(r.browser.load_ms)} unit="ms" good={r.browser.ok} icon />}
          </div>
          <p className="hint">
            {t('Measured {when}.', { when: formatDate(r.at) })} {t('HTTP and browser times include the local proxy; TCP and TLS are direct.')}
          </p>
        </>
      )}
      <section className="detail-section">
        <h3>{t('HTTP response time, last 24 hours')}</h3>
        <ErrorNotice error={error} />
        {history ? <HistoryChart history={history} /> : <div className="skeleton" style={{ height: 180 }} />}
        {history && <span className="hint">{t('{n} measurements · {ok} healthy', { n: history.length, ok: healthy })}</span>}
      </section>
      {r?.errors && r.errors.length > 0 && (
        <section className="detail-section">
          <h3>{t('Errors')}</h3>
          {r.errors.map((e, i) => (
            <div key={i} className="notice notice-error">
              <b>{e.stage.toUpperCase()}</b> {e.message}
            </div>
          ))}
        </section>
      )}
      {r && (r.tcp_error || r.http_error || r.browser?.error) && (
        <section className="detail-section">
          <h3>{t('Problems')}</h3>
          {[r.tcp_error, r.http_error, r.browser?.error].filter(Boolean).map((m) => (
            <div key={m} className="notice notice-error">
              {t(m!)}
            </div>
          ))}
        </section>
      )}
      {r?.dns && (
        <section className="detail-section">
          <h3>DNS</h3>
          <dl className="kv">
            <dt>{t('Addresses')}</dt>
            <dd className="chip-list">{r.dns.addresses.map((a) => <span key={a} className="mono-chip">{a}</span>)}</dd>
            <dt>{t('Resolvers')}</dt>
            <dd className="chip-list">{r.dns.servers?.map((a) => <span key={a} className="mono-chip">{a}</span>) ?? '—'}</dd>
            {r.remote_ip && (
              <>
                <dt>{t('Connected to')}</dt>
                <dd className="addr">{r.remote_ip}</dd>
              </>
            )}
          </dl>
        </section>
      )}
      {r?.tls && (
        <section className="detail-section">
          <h3>{t('Certificate')}</h3>
          {!r.tls.valid && <div className="notice notice-error">{t(r.tls.error ?? 'certificate verification failed')}</div>}
          <dl className="kv">
            <dt>{t('Subject')}</dt>
            <dd>{r.tls.subject ?? '—'}</dd>
            <dt>{t('Issuer')}</dt>
            <dd>{r.tls.issuer ?? '—'}</dd>
            <dt>{t('Names')}</dt>
            <dd>{r.tls.san?.join(', ') || '—'}</dd>
            <dt>{t('Expires')}</dt>
            <dd>{r.tls.not_after ? `${formatDate(r.tls.not_after, false)} · ${t('{n} days', { n: r.tls.expires_days ?? 0 })}` : '—'}</dd>
            <dt>{t('Protocol')}</dt>
            <dd>{[r.tls.version, r.tls.cipher].filter(Boolean).join(' · ') || '—'}</dd>
            <dt>SHA-256</dt>
            <dd className="addr">{r.tls.sha256 ?? '—'}</dd>
          </dl>
        </section>
      )}
      {r?.browser && (r.browser.bad_responses?.length || r.browser.failed_requests?.length) ? (
        <section className="detail-section">
          <h3>{t('Page resources')}</h3>
          {r.browser.bad_responses?.map((b) => (
            <div key={b.url} className="notice notice-warning">
              <b>{b.status}</b> <span className="addr">{b.url}</span>
            </div>
          ))}
          {r.browser.failed_requests?.map((f, i) => (
            <div key={i} className="notice notice-warning">
              {f.error}
            </div>
          ))}
        </section>
      ) : null}
      {r?.trace && (
        <section className="detail-section">
          <h3>{t('Route')}</h3>
          {r.trace.output ? <pre className="trace">{r.trace.output}</pre> : <div className="notice">{t(r.trace.error ?? 'traceroute unavailable or timed out')}</div>}
        </section>
      )}
      {r && (
        <details className="raw">
          <summary>{t('Raw measurement')}</summary>
          <pre className="trace">{JSON.stringify(r, null, 2)}</pre>
        </details>
      )}
    </Drawer>
  );
}

export function OverviewPage() {
  const t = useT();
  const { account } = useAuth();
  const admin = isAdmin(account);
  const { data, error, reload } = useLoad(() => get<Dashboard>('/api/dashboard'));
  const refresh = useCallback(() => void reload(), [reload]);
  usePoll(refresh, 15000);
  const [filter, setFilter] = useState('');
  const [view, setView] = useState<'all' | 'issues'>('all');
  const [editing, setEditing] = useState<Site | null | undefined>(undefined);
  const [detail, setDetail] = useState<{ site: Site; iface: HostInterface } | null>(null);
  const [busy, setBusy] = useState('');
  useEffect(() => {
    document.title = `${t('Availability')} · VLAN Analizer`;
  }, [t]);

  const interfaces = useMemo(() => data?.interfaces.filter((i) => i.probeable) ?? [], [data]);
  const lookup = useMemo(() => new Map((data?.results ?? []).map((r) => [`${r.site_id}/${r.interface}`, r])), [data]);
  const sites = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return (data?.sites ?? [])
      .filter((s) => `${s.name} ${s.url} ${s.source}`.toLowerCase().includes(q))
      .filter((s) => view === 'all' || interfaces.some((i) => ['down', 'degraded', 'error'].includes(lookup.get(`${s.id}/${i.interface}`)?.status ?? '')))
      .sort((a, b) => a.name.localeCompare(b.name));
  }, [data, filter, view, interfaces, lookup]);

  const results = data?.results ?? [];
  const activeSites = data?.sites.filter(active) ?? [];
  const healthy = results.filter((r) => r.status === 'up').length;
  const issues = results.filter((r) => ['down', 'degraded', 'error'].includes(r.status)).length;
  const warnings: string[] = [];
  if (data) {
    warnings.push(...data.worker.gatus_warnings, ...(data.worker.kuma_warnings ?? []));
    if (data.worker.error) warnings.push(t(data.worker.error));
    if (!interfaces.length) warnings.push(t('No probeable IPv4 interfaces found. Check host networking and the monitoring settings.'));
    const stale = activeSites.length * interfaces.length - results.filter((r) => r.status !== 'unknown').length;
    if (stale > 0) warnings.push(t('Paths without a fresh measurement: {n}.', { n: stale }));
  }

  const action = async (key: string, path: string) => {
    setBusy(key);
    try {
      const r = await post<{ queued?: boolean; imported?: number; warnings?: string[] }>(path);
      toast(r.queued ? t('Check cycle queued') : t('Imported {n} websites', { n: r.imported ?? 0 }) + (r.warnings?.length ? `\n${r.warnings.join('\n')}` : ''));
      await reload();
    } catch (e) {
      toastError(e);
    } finally {
      setBusy('');
    }
  };

  return (
    <>
      <PageHead
        title={t('Availability')}
        subtitle={t('One destination, every VLAN: reachability from each host interface.')}
        actions={
          admin && (
            <>
              <button type="button" className="btn btn-ghost" disabled={!!busy} onClick={() => action('gatus', '/api/gatus/sync')}>
                <RefreshCw size={16} className={busy === 'gatus' ? 'spin' : ''} />
                {t('Sync Gatus')}
              </button>
              <button type="button" className="btn btn-ghost" disabled={!!busy} onClick={() => action('kuma', '/api/kuma/sync')}>
                <RefreshCw size={16} className={busy === 'kuma' ? 'spin' : ''} />
                {t('Sync Kuma')}
              </button>
              <button type="button" className="btn" disabled={!!busy} onClick={() => action('run', '/api/checks/run')}>
                <Play size={16} />
                {t('Run checks')}
              </button>
              <button type="button" className="btn btn-primary" data-testid="add-site" onClick={() => setEditing(null)}>
                <Plus size={16} />
                {t('Add website')}
              </button>
            </>
          )
        }
      />
      {error && !data ? <ErrorNotice error={error} /> : null}
      <AnimatePresence initial={false}>
        {warnings.length > 0 && (
          <motion.div className="notice notice-warning" role="status" initial={{ opacity: 0, y: -6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -6 }}>
            <div className="row" style={{ alignItems: 'flex-start', flexWrap: 'nowrap' }}>
              <TriangleAlert size={16} className="icon" style={{ color: 'var(--warn)', marginTop: 2 }} />
              <div className="stack" style={{ gap: 2 }}>
                {warnings.map((w, i) => (
                  <span key={i}>{w}</span>
                ))}
              </div>
            </div>
          </motion.div>
        )}
      </AnimatePresence>
      <div className="tiles stagger">
        <Tile icon={<Globe size={17} />} label={t('Monitored websites')} value={activeSites.length} foot={t('Manual, Gatus and Kuma')} />
        <Tile icon={<Network size={17} />} label={t('Probe interfaces')} value={interfaces.length} foot={t('Up, with IPv4')} tone="info" />
        <Tile icon={<CircleCheck size={17} />} label={t('Healthy paths')} value={healthy} foot={t('HTTP and page checks passed')} tone="ok" />
        <Tile icon={<CircleAlert size={17} />} label={t('Needs attention')} value={issues} foot={t('Down or degraded paths')} tone={issues ? 'error' : undefined} />
      </div>
      <div className="card matrix-card">
        <div className="matrix-toolbar">
          <div className="toolbar" style={{ flex: 1 }}>
            <div className="search">
              <Search size={16} />
              <input className="input" placeholder={t('Search websites')} aria-label={t('Search websites')} value={filter} onChange={(e) => setFilter(e.target.value)} />
            </div>
            <Segmented
              label={t('Show')}
              value={view}
              onChange={setView}
              options={[
                { value: 'all', label: t('All') },
                { value: 'issues', label: t('Issues') },
              ]}
            />
          </div>
          <span className="updated">
            <span className={`live-dot ${data?.worker.running ? '' : 'idle'}`} />
            {data?.worker.running ? t('Checks running') : t('Last cycle {when}', { when: formatAgo(data?.worker.last_cycle) })}
          </span>
        </div>
        <div className="matrix-toolbar" style={{ paddingTop: 10, paddingBottom: 10 }}>
          <div className="legend">
            {(
              [
                ['up', t('Healthy')],
                ['degraded', t('Degraded')],
                ['down', t('Down')],
                ['unknown', t('Unknown or stale')],
              ] as const
            ).map(([s, label]) => (
              <span key={s} className={`status-${s}`}>
                <span className="status-dot" />
                {label}
              </span>
            ))}
          </div>
        </div>
        {!data ? (
          <div style={{ padding: 16 }}>
            <div className="skeleton" style={{ height: 160 }} />
          </div>
        ) : sites.length === 0 ? (
          <Empty
            icon={<Globe className="icon" />}
            title={filter || view === 'issues' ? t('Nothing matches') : t('No websites yet')}
            text={filter || view === 'issues' ? undefined : t('Add a website or connect Gatus or Uptime Kuma to start monitoring.')}
            action={
              admin && !filter && view === 'all' ? (
                <button type="button" className="btn btn-primary" onClick={() => setEditing(null)}>
                  <Plus size={16} />
                  {t('Add website')}
                </button>
              ) : undefined
            }
          />
        ) : (
          <div className="matrix-scroll">
            <table className="matrix" data-testid="matrix">
              <thead>
                <tr>
                  <th>{t('Website')}</th>
                  {interfaces.map((i) => (
                    <th key={i.interface}>
                      {i.interface}
                      <small>{vlanLabel(t, i)}</small>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {sites.map((s) => (
                  <tr key={s.id}>
                    <td>
                      <div className="site-cell">
                        <SourceMark source={s.source} />
                        <div>
                          <strong>{s.name}</strong>
                          <small>
                            {!active(s) && <>{s.source_enabled === 0 ? t('Paused in source') : t('Paused')} · </>}
                            {s.url}
                          </small>
                        </div>
                        {admin && (
                          <button type="button" className="icon-btn" aria-label={t('Edit website')} title={t('Edit website')} onClick={() => setEditing(s)}>
                            <Pencil size={15} />
                          </button>
                        )}
                      </div>
                    </td>
                    {interfaces.map((i) => {
                      const r = lookup.get(`${s.id}/${i.interface}`);
                      const state: Status | 'paused' = active(s) ? (r?.status ?? 'unknown') : 'paused';
                      return (
                        <td key={i.interface}>
                          <div className="cell-result">
                            <StatusChip status={state} onClick={() => setDetail({ site: s, iface: i })} />
                            <span className="cell-latency">{r?.http?.total_ms != null ? `${Math.round(r.http.total_ms)} ${t('ms')} · HTTP ${r.http.code}` : t('Awaiting measurement')}</span>
                          </div>
                        </td>
                      );
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      <p className="hint">
        {t('Checks use the selected host interface and never fall back to the default route. Untagged interfaces do not imply VLAN 1.')} {data && t('Inventory updated {when}.', { when: formatAgo(data.inventory_at) })}
      </p>
      <AnimatePresence>{editing !== undefined && <SiteDialog key="site" site={editing} onClose={() => setEditing(undefined)} onSaved={refresh} />}</AnimatePresence>
      <AnimatePresence>{detail && <DetailDrawer key="detail" site={detail.site} iface={detail.iface} result={lookup.get(`${detail.site.id}/${detail.iface.interface}`)} onClose={() => setDetail(null)} />}</AnimatePresence>
    </>
  );
}

