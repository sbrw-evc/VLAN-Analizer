import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react';
import { ArrowDownToLine, ArrowUpToLine, Cable, HardDrive, LoaderCircle, Pencil, Plus, ServerOff, Trash2, TriangleAlert } from 'lucide-react';
import { AnimatePresence, motion } from 'motion/react';
import { del, get, post, put } from '../api/client';
import type { ManagedVlan, Trunk, VlanListing, VlanMode } from '../api/types';
import { ActionMenu, Confirm, Empty, ErrorNotice, Field, Modal, PageHead, Segmented, StatusChip, Switch } from '../components/ui';
import { formatDate, useT } from '../i18n';
import { useLoad, usePoll } from '../lib/auth';
import { toast, toastError } from '../lib/toast';

type Ready = Extract<VlanListing, { available: true }>;

const BACKEND_LABEL: Record<string, string> = {
  networkmanager: 'NetworkManager',
  netplan: 'netplan',
  ifupdown: 'ifupdown',
  'network-scripts': 'network-scripts',
  runtime: 'ip link',
};

/** Mirrors hostnet.ParseVLANs for the live preview; the server validates again. */
export function parseVlans(raw: string): number[] | string {
  const seen = new Set<number>();
  for (const part of raw.split(/[\s,;]+/).filter(Boolean)) {
    const m = /^(\d+)(?:-(\d+))?$/.exec(part);
    if (!m) return part;
    const a = Number(m[1]);
    const b = m[2] ? Number(m[2]) : a;
    if (a < 1 || b > 4094 || a > b) return part;
    for (let v = a; v <= b; v++) {
      seen.add(v);
      if (seen.size > 64) return 'limit';
    }
  }
  return [...seen].sort((x, y) => x - y);
}

export function vlanName(naming: 'dotted' | 'vlan', parent: string, vlan: number) {
  const dotted = `${parent}.${vlan}`;
  return naming === 'vlan' || dotted.length > 15 ? `vlan${vlan}` : dotted;
}

function splitDns(raw: string) {
  return raw
    .split(/[\s,;]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}

function ModeControl({ value, onChange, allowStatic }: { value: VlanMode; onChange: (m: VlanMode) => void; allowStatic: boolean }) {
  const t = useT();
  return (
    <Segmented<VlanMode>
      label={t('Addressing')}
      value={value}
      onChange={onChange}
      options={[
        { value: 'dhcp', label: 'DHCP' },
        ...(allowStatic ? [{ value: 'static' as VlanMode, label: t('Static') }] : []),
        { value: 'none', label: t('No address') },
      ]}
    />
  );
}

function AddressFields({ address, gateway, setAddress, setGateway }: { address: string; gateway: string; setAddress: (v: string) => void; setGateway: (v: string) => void }) {
  const t = useT();
  return (
    <motion.div className="grid-2" initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: 'auto' }} exit={{ opacity: 0, height: 0 }}>
      <Field label={t('IPv4 address')} hint={t('CIDR, for example 10.20.0.5/24')}>
        <input className="input" name="address" value={address} onChange={(e) => setAddress(e.target.value)} placeholder="10.20.0.5/24" required />
      </Field>
      <Field label={t('Gateway')} hint={t('Optional. Adds a default route with a high metric for probes on this VLAN.')}>
        <input className="input" name="gateway" value={gateway} onChange={(e) => setGateway(e.target.value)} placeholder="10.20.0.1" />
      </Field>
    </motion.div>
  );
}

function CreateDialog({ data, parent: initialParent, onClose, onDone }: { data: Ready; parent?: string; onClose: () => void; onDone: () => void }) {
  const t = useT();
  const [parent, setParent] = useState(initialParent ?? data.trunks.find((tr) => tr.addresses.length === 0)?.name ?? data.trunks[0]?.name ?? '');
  const [vlans, setVlans] = useState('');
  const [naming, setNaming] = useState<'dotted' | 'vlan'>('dotted');
  const [mode, setMode] = useState<VlanMode>('dhcp');
  const [address, setAddress] = useState('');
  const [gateway, setGateway] = useState('');
  const [mtu, setMtu] = useState('');
  const [metric, setMetric] = useState('');
  const [dns, setDns] = useState('');
  const [monitor, setMonitor] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [failed, setFailed] = useState<{ vlan?: number; name?: string; detail: string }[]>([]);
  const parsed = useMemo(() => (vlans.trim() ? parseVlans(vlans) : []), [vlans]);
  const ids = Array.isArray(parsed) ? parsed : [];
  const single = ids.length <= 1;
  useEffect(() => {
    if (!single && mode === 'static') setMode('dhcp');
  }, [single, mode]);
  const trunk = data.trunks.find((tr) => tr.name === parent);
  const taken = new Set(trunk?.vlans.map((v) => v.vlan));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setFailed([]);
    try {
      const r = await post<{ created: ManagedVlan[]; failed: { vlan?: number; name?: string; detail: string }[] }>('/api/network/vlans', {
        parent,
        vlans,
        naming,
        mode,
        address: mode === 'static' ? address : '',
        gateway: mode === 'static' ? gateway : '',
        mtu: mtu ? Number(mtu) : 0,
        metric: metric && single ? Number(metric) : 0,
        dns_servers: splitDns(dns),
        monitor,
      });
      toast(t('Created {n} VLAN interfaces', { n: r.created.length }));
      onDone();
      if (r.failed.length) setFailed(r.failed);
      else onClose();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={
        <>
          <Cable size={20} />
          {t('Create VLAN interfaces')}
        </>
      }
      width={620}
      onClose={onClose}
      testId="vlan-dialog"
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {t('Cancel')}
          </button>
          <button type="submit" form="vlan-form" className="btn btn-primary" disabled={busy || !parent || ids.length === 0}>
            {busy && <LoaderCircle size={16} className="spin" />}
            {ids.length > 1 ? t('Create {n} interfaces', { n: ids.length }) : t('Create interface')}
          </button>
        </>
      }
    >
      <form id="vlan-form" className="stack" style={{ gap: 14 }} onSubmit={submit}>
        <div className="grid-2">
          <Field label={t('Trunk interface')} hint={t('The port connected to the switch trunk. Tagged frames for each VLAN arrive here.')}>
            <select className="input" name="parent" value={parent} onChange={(e) => setParent(e.target.value)} required>
              {data.trunks.map((tr) => (
                <option key={tr.name} value={tr.name}>
                  {tr.name} · {tr.kind}
                  {tr.addresses.length ? ` · ${tr.addresses[0]}` : ''}
                </option>
              ))}
            </select>
          </Field>
          <Field label={t('VLAN IDs')} hint={t('One ID, a list or ranges: 20, 30-35. Up to 64 at once.')}>
            <input className="input" name="vlans" value={vlans} onChange={(e) => setVlans(e.target.value)} placeholder="20, 30-35" required autoFocus />
          </Field>
        </div>
        {typeof parsed === 'string' && <div className="notice notice-error">{parsed === 'limit' ? t('At most 64 VLANs per request') : t('Invalid VLAN list item {item}', { item: parsed })}</div>}
        {ids.length > 0 && parent && (
          <div className="stack" style={{ gap: 6 }}>
            <span className="field-label" style={{ margin: 0 }}>
              {t('Interfaces to create')}
            </span>
            <div className="preview-list">
              <AnimatePresence initial={false}>
                {ids.map((v) => (
                  <motion.span key={v} layout className={`vlan-tag ${taken.has(v) ? 'down' : 'managed'}`} initial={{ opacity: 0, scale: 0.8 }} animate={{ opacity: 1, scale: 1 }} exit={{ opacity: 0, scale: 0.8 }} transition={{ duration: 0.15 }} title={taken.has(v) ? t('Already exists on this trunk') : undefined}>
                    {vlanName(naming, parent, v)}
                  </motion.span>
                ))}
              </AnimatePresence>
            </div>
            {ids.some((v) => taken.has(v)) && <span className="hint">{t('Grey VLANs already exist on this trunk and will be skipped with an error.')}</span>}
          </div>
        )}
        <div className="row-between">
          <Segmented
            label={t('Naming')}
            value={naming}
            onChange={setNaming}
            options={[
              { value: 'dotted', label: `${parent || 'eth0'}.<id>` },
              { value: 'vlan', label: 'vlan<id>' },
            ]}
          />
          <ModeControl value={mode} onChange={setMode} allowStatic={single} />
        </div>
        <AnimatePresence initial={false}>{mode === 'static' && <AddressFields address={address} gateway={gateway} setAddress={setAddress} setGateway={setGateway} />}</AnimatePresence>
        {mode === 'dhcp' && <span className="hint">{t('DHCP routes get a metric above the primary route, so the host keeps its default path.')}</span>}
        {mode === 'none' && <span className="hint">{t('The interface comes up without an address. It is inventory only and cannot be probed until it has IPv4.')}</span>}
        <div className="grid-3">
          <Field label="MTU" hint={t('Empty keeps the default.')}>
            <input className="input" name="mtu" type="number" min={576} max={9216} value={mtu} onChange={(e) => setMtu(e.target.value)} placeholder="1500" />
          </Field>
          <Field label={t('Route metric')} hint={single ? t('Default: 2000 + VLAN ID.') : t('2000 + VLAN ID for each.')}>
            <input className="input" name="metric" type="number" min={1} max={65535} value={single ? metric : ''} disabled={!single} onChange={(e) => setMetric(e.target.value)} placeholder={ids.length === 1 ? String(2000 + ids[0]) : '2000+'} />
          </Field>
          <Field label={t('DNS servers')} hint={t('Optional, up to four IPv4.')}>
            <input className="input" name="dns" value={dns} onChange={(e) => setDns(e.target.value)} placeholder="10.20.0.53" />
          </Field>
        </div>
        <Switch name="monitor" checked={monitor} onChange={setMonitor} label={t('Probe websites through these interfaces')} hint={t('Interfaces with IPv4 join the availability matrix on the next cycle.')} />
        <ErrorNotice error={error} />
        {failed.map((f, i) => (
          <div key={i} className="notice notice-error">
            {f.name && <b>{f.name}: </b>}
            {f.detail}
          </div>
        ))}
      </form>
    </Modal>
  );
}

function EditDialog({ item, dns: currentDns, onClose, onDone }: { item: ManagedVlan; dns: string[]; onClose: () => void; onDone: () => void }) {
  const t = useT();
  const [mode, setMode] = useState<VlanMode>(item.mode);
  const [address, setAddress] = useState(item.address ?? '');
  const [gateway, setGateway] = useState(item.gateway ?? '');
  const [mtu, setMtu] = useState(item.mtu ? String(item.mtu) : '');
  const [metric, setMetric] = useState(String(item.metric ?? 2000 + item.vlan));
  const [dns, setDns] = useState(currentDns.join(', '));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await put(`/api/network/vlans/${encodeURIComponent(item.name)}`, {
        mode,
        address: mode === 'static' ? address : '',
        gateway: mode === 'static' ? gateway : '',
        mtu: mtu ? Number(mtu) : 0,
        metric: Number(metric) || 0,
        dns_servers: splitDns(dns),
      });
      toast(t('{name} updated', { name: item.name }));
      onDone();
      onClose();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      title={t('Edit {name}', { name: item.name })}
      width={560}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {t('Cancel')}
          </button>
          <button type="submit" form="vlan-edit" className="btn btn-primary" disabled={busy}>
            {busy && <LoaderCircle size={16} className="spin" />}
            {t('Save and apply')}
          </button>
        </>
      }
    >
      <form id="vlan-edit" className="stack" style={{ gap: 14 }} onSubmit={submit}>
        <div className="row">
          <span className="pill">{t('Trunk {parent}', { parent: item.parent })}</span>
          <span className="vlan-tag managed">VLAN {item.vlan}</span>
        </div>
        <ModeControl value={mode} onChange={setMode} allowStatic />
        <AnimatePresence initial={false}>{mode === 'static' && <AddressFields address={address} gateway={gateway} setAddress={setAddress} setGateway={setGateway} />}</AnimatePresence>
        <div className="grid-3">
          <Field label="MTU">
            <input className="input" type="number" min={576} max={9216} value={mtu} onChange={(e) => setMtu(e.target.value)} placeholder="1500" />
          </Field>
          <Field label={t('Route metric')}>
            <input className="input" type="number" min={1} max={65535} value={metric} onChange={(e) => setMetric(e.target.value)} />
          </Field>
          <Field label={t('DNS servers')}>
            <input className="input" value={dns} onChange={(e) => setDns(e.target.value)} placeholder={t('Host resolvers')} />
          </Field>
        </div>
        <div className="notice notice-warning">{t('Applying re-creates the interface; probes through it pause for a few seconds.')}</div>
        <ErrorNotice error={error} />
      </form>
    </Modal>
  );
}

function TrunkCard({ trunk, managed, onAdd }: { trunk: Trunk; managed: Set<string>; onAdd: () => void }) {
  const t = useT();
  return (
    <div className="card trunk" data-testid={`trunk-${trunk.name}`}>
      <div className="iface-head">
        <span className="iface-icon">
          <HardDrive size={19} />
        </span>
        <div>
          <strong>{trunk.name}</strong>
          <small>
            {trunk.kind} · MTU {trunk.mtu}
            {trunk.addresses.length ? ` · ${trunk.addresses.join(', ')}` : ''}
          </small>
        </div>
        <StatusChip status={trunk.up ? 'up' : 'down'}>{trunk.state}</StatusChip>
      </div>
      <div className="trunk-wire" aria-hidden />
      <div className="chip-list">
        {trunk.vlans.length === 0 && <span className="hint">{t('No tagged VLANs yet')}</span>}
        {trunk.vlans.map((v) => (
          <span key={v.name} className={`vlan-tag ${managed.has(v.name) ? 'managed' : ''} ${v.up ? '' : 'down'}`} title={`${v.name}${v.addresses.length ? ` · ${v.addresses.join(', ')}` : ''}`}>
            <span className="vlan-id">{v.vlan}</span>
            <span className="small">{v.name}</span>
          </span>
        ))}
      </div>
      <div>
        <button type="button" className="btn btn-sm" onClick={onAdd}>
          <Plus size={15} />
          {t('Add VLANs')}
        </button>
      </div>
    </div>
  );
}

export function VlansPage() {
  const t = useT();
  const { data, error, reload } = useLoad(() => get<VlanListing>('/api/network/vlans'));
  const refresh = useCallback(() => void reload(), [reload]);
  usePoll(refresh, 20000);
  const [create, setCreate] = useState<{ parent?: string } | null>(null);
  const [editing, setEditing] = useState<ManagedVlan | null>(null);
  const [removing, setRemoving] = useState<ManagedVlan | null>(null);
  const [busy, setBusy] = useState('');
  useEffect(() => {
    document.title = `${t('VLAN interfaces')} · VLAN Analizer`;
  }, [t]);

  const ready = data && data.available ? data : null;
  const managed = useMemo(() => new Set(ready?.interfaces.map((i) => i.name)), [ready]);
  const monitored = (i: ManagedVlan) => (ready!.monitoring.interfaces.length ? ready!.monitoring.interfaces.includes(i.name) : i.present && i.live.addresses.length > 0);

  const setState = async (i: ManagedVlan, up: boolean) => {
    setBusy(i.name);
    try {
      await post(`/api/network/vlans/${encodeURIComponent(i.name)}/${up ? 'up' : 'down'}`);
      toast(up ? t('{name} is up', { name: i.name }) : t('{name} is down', { name: i.name }));
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
        title={t('VLAN interfaces')}
        subtitle={t('Tagged 802.1Q subinterfaces on trunk ports, created and persisted through the host’s own network configuration.')}
        actions={
          ready && (
            <button type="button" className="btn btn-primary" data-testid="create-vlan" disabled={ready.trunks.length === 0 || !!ready.environment.error} onClick={() => setCreate({})}>
              <Plus size={16} />
              {t('Create VLAN interfaces')}
            </button>
          )
        }
      />
      <ErrorNotice error={error} />
      {!data && !error && <div className="skeleton" style={{ height: 120 }} />}
      {data && !data.available && (
        <div className="card" data-testid="agent-unavailable">
          <Empty
            icon={<ServerOff className="icon" />}
            title={t('The host network agent is not running')}
            text={
              <>
                {t('VLAN interfaces are created by the netagent service of the Compose file. It runs privileged in the host namespaces and is reachable only through a local socket.')}
                <br />
                <code>docker compose up -d netagent</code>
              </>
            }
            action={
              <button type="button" className="btn" onClick={refresh}>
                {t('Check again')}
              </button>
            }
          />
        </div>
      )}
      {ready && ready.environment.error && (
        <div className="card" data-testid="host-environment">
          <Empty icon={<ServerOff className="icon" />} title={t('The host network agent cannot reach the host')} text={t(ready.environment.error)} action={<button type="button" className="btn" onClick={refresh}>{t('Check again')}</button>} />
        </div>
      )}
      {ready && !ready.environment.error && (
        <>
          <div className="card env-card" data-testid="host-environment">
            <span className="iface-icon">
              <HardDrive size={22} />
            </span>
            <div>
              <strong>{ready.environment.distro.name || t('Unknown distribution')}</strong>
              <span className="muted small">
                {t('Configuration written through {backend}', { backend: BACKEND_LABEL[ready.environment.backend] ?? ready.environment.backend })}
              </span>
            </div>
            <div className="env-facts">
              <span className={`pill ${ready.environment.supported ? 'pill-ok' : 'pill-warn'}`}>{ready.environment.supported ? t('Supported distribution') : t('Untested distribution')}</span>
              <span className={`pill ${ready.environment.persistent ? 'pill-ok' : 'pill-warn'}`}>{ready.environment.persistent ? t('Survives reboots') : t('Runtime only')}</span>
            </div>
          </div>
          {ready.environment.warnings.map((w) => (
            <div key={w} className="notice notice-warning">
              <TriangleAlert size={15} className="icon" /> {t(w)}
            </div>
          ))}
          <h2 className="section-title">{t('Trunk ports')}</h2>
          {ready.trunks.length === 0 ? (
            <div className="card">
              <Empty icon={<HardDrive className="icon" />} title={t('No trunk-capable interfaces')} text={t('Physical interfaces, bonds and bridges can carry tagged VLANs. Container interfaces are excluded.')} />
            </div>
          ) : (
            <div className="trunk-grid stagger">
              {ready.trunks.map((tr) => (
                <TrunkCard key={tr.name} trunk={tr} managed={managed} onAdd={() => setCreate({ parent: tr.name })} />
              ))}
            </div>
          )}
          <h2 className="section-title">{t('Managed interfaces')}</h2>
          <div className="table-card">
            {ready.interfaces.length === 0 ? (
              <Empty
                icon={<Cable className="icon" />}
                title={t('No VLAN interfaces created here yet')}
                text={t('Interfaces created on this page are written to the host configuration and appear in the availability matrix once they have IPv4.')}
                action={
                  ready.trunks.length > 0 && (
                    <button type="button" className="btn btn-primary" onClick={() => setCreate({})}>
                      <Plus size={16} />
                      {t('Create VLAN interfaces')}
                    </button>
                  )
                }
              />
            ) : (
              <table className="data-table" data-testid="managed-vlans">
                <thead>
                  <tr>
                    <th>{t('Interface')}</th>
                    <th>VLAN</th>
                    <th>{t('Addressing')}</th>
                    <th>{t('State')}</th>
                    <th>{t('Monitoring')}</th>
                    <th>{t('Persistence')}</th>
                    <th className="actions-cell" />
                  </tr>
                </thead>
                <tbody>
                  {ready.interfaces.map((i) => {
                    const dns = ready.monitoring.dns_servers[i.name] ?? [];
                    return (
                      <tr key={i.name}>
                        <td>
                          <div className="cell-main">
                            <div>
                              <strong>{i.name}</strong>
                              <small>{t('on {parent}', { parent: i.parent })}</small>
                            </div>
                          </div>
                        </td>
                        <td>
                          <span className="vlan-tag managed">{i.vlan}</span>
                        </td>
                        <td>
                          <div className="cell-main">
                            <div>
                              <span>{i.mode === 'dhcp' ? 'DHCP' : i.mode === 'static' ? t('Static') : t('No address')}</span>
                              <small className="addr">{i.live.addresses?.join(', ') || i.address || '—'}</small>
                            </div>
                          </div>
                        </td>
                        <td>
                          {busy === i.name ? (
                            <LoaderCircle size={16} className="spin" />
                          ) : !i.present ? (
                            <StatusChip status="down">{t('Missing')}</StatusChip>
                          ) : (
                            <StatusChip status={i.live.up ? 'up' : 'unknown'}>{i.live.up ? t('Up') : t('Down')}</StatusChip>
                          )}
                        </td>
                        <td>
                          <div className="cell-main">
                            <div>
                              <span>{monitored(i) ? t('Probed') : t('Not probed')}</span>
                              <small>{dns.length ? `DNS ${dns.join(', ')}` : t('Host resolvers')}</small>
                            </div>
                          </div>
                        </td>
                        <td>
                          <div className="cell-main">
                            <div>
                              <span>{BACKEND_LABEL[i.backend] ?? i.backend}</span>
                              <small>{formatDate(i.created, false)}</small>
                            </div>
                          </div>
                        </td>
                        <td className="actions-cell">
                          <ActionMenu label={t('Actions for {name}', { name: i.name })}>
                            {(close) => (
                              <>
                                {i.present && i.live.up ? (
                                  <button type="button" role="menuitem" className="um-item" onClick={() => (close(), setState(i, false))}>
                                    <ArrowDownToLine size={17} />
                                    {t('Bring down')}
                                  </button>
                                ) : (
                                  <button type="button" role="menuitem" className="um-item" onClick={() => (close(), setState(i, true))}>
                                    <ArrowUpToLine size={17} />
                                    {t('Bring up')}
                                  </button>
                                )}
                                <button type="button" role="menuitem" className="um-item" onClick={() => (close(), setEditing(i))}>
                                  <Pencil size={17} />
                                  {t('Edit addressing')}
                                </button>
                                <div className="um-sep" />
                                <button type="button" role="menuitem" className="um-item danger" onClick={() => (close(), setRemoving(i))}>
                                  <Trash2 size={17} />
                                  {t('Delete interface')}
                                </button>
                              </>
                            )}
                          </ActionMenu>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            )}
          </div>
        </>
      )}
      <AnimatePresence>{create && ready && <CreateDialog key="create" data={ready} parent={create.parent} onClose={() => setCreate(null)} onDone={refresh} />}</AnimatePresence>
      <AnimatePresence>{editing && ready && <EditDialog key="edit" item={editing} dns={ready.monitoring.dns_servers[editing.name] ?? []} onClose={() => setEditing(null)} onDone={refresh} />}</AnimatePresence>
      <AnimatePresence>
        {removing && (
          <Confirm
            key="remove"
            title={t('Delete {name}', { name: removing.name })}
            text={t('The interface and its configuration on the host are removed. Probes through VLAN {vlan} stop.', { vlan: removing.vlan })}
            confirm={t('Delete interface')}
            danger
            onClose={() => setRemoving(null)}
            onConfirm={async () => {
              await del(`/api/network/vlans/${encodeURIComponent(removing.name)}`);
              toast(t('{name} deleted', { name: removing.name }));
              await reload();
            }}
          />
        )}
      </AnimatePresence>
    </>
  );
}
