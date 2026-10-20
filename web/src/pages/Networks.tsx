import { useCallback, useEffect, useMemo, useState } from 'react';
import { Cable, EthernetPort, GitFork, Layers, Network, Search } from 'lucide-react';
import { Link } from 'react-router-dom';
import { get } from '../api/client';
import type { Dashboard, HostInterface } from '../api/types';
import { Empty, ErrorNotice, PageHead, Segmented } from '../components/ui';
import { formatAgo, useT } from '../i18n';
import { isAdmin, useAuth, useLoad, usePoll } from '../lib/auth';

interface BridgeVlan {
  ifname?: string;
  vlans?: { vlan: number; vlanEnd?: number; flags?: string[] }[];
}

function KindIcon({ kind }: { kind: string }) {
  const I = kind === 'vlan' ? Cable : kind === 'bridge' ? GitFork : kind === 'bond' ? Layers : EthernetPort;
  return (
    <span className={`iface-icon kind-${kind}`}>
      <I size={19} />
    </span>
  );
}

function InterfaceCard({ i }: { i: HostInterface }) {
  const t = useT();
  const up = i.state === 'UP' || i.state === 'UNKNOWN';
  return (
    <div className="card iface-card">
      <div className="iface-head">
        <KindIcon kind={i.kind} />
        <div>
          <strong>{i.interface}</strong>
          <small>
            {i.kind}
            {i.parent != null && i.parent !== '' ? ` · ${t('on {parent}', { parent: String(i.parent) })}` : ''}
          </small>
        </div>
        {i.vlan_id != null && <span className="vlan-tag">VLAN {i.vlan_id}</span>}
      </div>
      <div className="row">
        <span className={`status-chip ${up ? 'status-up' : 'status-down'}`}>
          <span className="status-dot" />
          {i.state}
        </span>
        <span className={`pill ${i.probeable ? 'pill-accent' : ''}`}>{i.probeable ? t('Probing') : t('Not eligible')}</span>
      </div>
      <dl className="kv">
        <dt>IPv4</dt>
        <dd className="chip-list">{i.addresses.length ? i.addresses.map((a) => <span key={a} className="mono-chip">{a}</span>) : '—'}</dd>
        {i.ipv6.length > 0 && (
          <>
            <dt>IPv6</dt>
            <dd className="chip-list">{i.ipv6.map((a) => <span key={a} className="mono-chip">{a}</span>)}</dd>
          </>
        )}
        <dt>MAC</dt>
        <dd className="addr">{i.mac || '—'}</dd>
        <dt>MTU</dt>
        <dd>{i.mtu}</dd>
        {i.master != null && i.master !== '' && (
          <>
            <dt>{t('Member of')}</dt>
            <dd>{String(i.master)}</dd>
          </>
        )}
        <dt>{t('Routes')}</dt>
        <dd>{i.routes.length ? i.routes.slice(0, 4).map((r, n) => <div key={n} className="addr">{[r.dst, r.gateway ? `via ${r.gateway}` : '', r.table && r.table !== 'main' ? `table ${r.table}` : '', r.metric != null ? `metric ${r.metric}` : ''].filter(Boolean).join(' ')}</div>) : '—'}</dd>
      </dl>
    </div>
  );
}

export function NetworksPage() {
  const t = useT();
  const { account } = useAuth();
  const { data, error, reload } = useLoad(() => get<Dashboard>('/api/dashboard'));
  const refresh = useCallback(() => void reload(), [reload]);
  usePoll(refresh, 30000);
  const [q, setQ] = useState('');
  const [view, setView] = useState<'all' | 'vlan' | 'probe'>('all');
  useEffect(() => {
    document.title = `${t('Networks')} · VLAN Analizer`;
  }, [t]);
  const list = useMemo(
    () =>
      (data?.interfaces ?? [])
        .filter((i) => view === 'all' || (view === 'vlan' ? i.vlan_id != null : i.probeable))
        .filter((i) => `${i.interface} ${i.addresses.join(' ')} ${i.vlan_id ?? ''}`.toLowerCase().includes(q.trim().toLowerCase())),
    [data, q, view],
  );
  const bridge = (data?.bridge_vlans ?? []) as BridgeVlan[];

  return (
    <>
      <PageHead
        title={t('Networks and VLANs')}
        subtitle={t('Configured Linux interfaces, 802.1Q VLANs and bridge VLAN membership. IPv6 addresses are inventory only.')}
        actions={
          isAdmin(account) && (
            <Link to="/vlans" className="btn btn-primary">
              <Cable size={16} />
              {t('Manage VLAN interfaces')}
            </Link>
          )
        }
      />
      <ErrorNotice error={error} />
      <div className="toolbar">
        <div className="search">
          <Search size={16} />
          <input className="input" placeholder={t('Search interfaces')} aria-label={t('Search interfaces')} value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <Segmented
          label={t('Show')}
          value={view}
          onChange={setView}
          options={[
            { value: 'all', label: t('All') },
            { value: 'vlan', label: t('VLANs') },
            { value: 'probe', label: t('Probing') },
          ]}
        />
        <span className="spacer" />
        {data && <span className="updated">{t('Inventory updated {when}.', { when: formatAgo(data.inventory_at) })}</span>}
      </div>
      {!data ? (
        <div className="iface-grid">
          {[0, 1, 2].map((n) => (
            <div key={n} className="skeleton" style={{ height: 220 }} />
          ))}
        </div>
      ) : list.length === 0 ? (
        <div className="card">
          <Empty icon={<Network className="icon" />} title={t('No interfaces match')} />
        </div>
      ) : (
        <div className="iface-grid stagger" data-testid="interfaces">
          {list.map((i) => (
            <InterfaceCard key={i.interface} i={i} />
          ))}
        </div>
      )}
      <h2 className="section-title">{t('Bridge VLAN membership')}</h2>
      <div className="table-card">
        {bridge.length === 0 ? (
          <div className="empty-state-sm">{t('No bridge VLAN filtering on this host.')}</div>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>{t('Port')}</th>
                <th>{t('VLANs')}</th>
              </tr>
            </thead>
            <tbody>
              {bridge.map((b, n) => (
                <tr key={b.ifname ?? n}>
                  <td>
                    <strong>{b.ifname ?? '—'}</strong>
                  </td>
                  <td>
                    <div className="chip-list">
                      {(b.vlans ?? []).map((v) => (
                        <span key={v.vlan} className="vlan-tag" title={(v.flags ?? []).join(', ')}>
                          {v.vlanEnd ? `${v.vlan}–${v.vlanEnd}` : v.vlan}
                          {v.flags?.includes('PVID') && <span className="muted small">PVID</span>}
                          {v.flags?.includes('Egress Untagged') && <span className="muted small">{t('untagged')}</span>}
                        </span>
                      ))}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
