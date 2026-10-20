import { useEffect } from 'react';
import { Webhook } from 'lucide-react';
import { PageHead } from '../../components/ui';
import { useT } from '../../i18n';

export function IntegrationsPage() {
  const t = useT();
  const origin = window.location.origin;
  useEffect(() => {
    document.title = `${t('Zabbix')} · VLAN Analizer`;
  }, [t]);
  return (
    <>
      <PageHead icon={<Webhook size={22} />} title={t('Zabbix')} subtitle={t('HTTP agent items and low-level discovery for every website and interface pair.')} />
      <div className="card">
        <div className="card-head">
          <h2>{t('Endpoints')}</h2>
        </div>
        <div className="card-body">
          <dl className="kv">
            <dt>{t('Snapshot')}</dt>
            <dd>
              <code>{origin}/api/integrations/zabbix</code>
            </dd>
            <dt>{t('Discovery')}</dt>
            <dd>
              <code>{origin}/api/integrations/zabbix/discovery</code>
            </dd>
            <dt>{t('Authentication')}</dt>
            <dd>{t('Bearer token from OpenBao (zabbix_token in the app secret).')}</dd>
          </dl>
        </div>
      </div>
      <div className="table-card">
        <table className="data-table">
          <thead>
            <tr>
              <th>{t('Value of available')}</th>
              <th>{t('Meaning')}</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td>1</td>
              <td>{t('Healthy')}</td>
            </tr>
            <tr>
              <td>2</td>
              <td>{t('Degraded')}</td>
            </tr>
            <tr>
              <td>0</td>
              <td>{t('Down')}</td>
            </tr>
            <tr>
              <td>−1</td>
              <td>{t('Unknown or stale')}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p className="hint">{t('Missing timings are null. Discovery macros: {#SITE_ID}, {#SITE}, {#INTERFACE}, {#VLAN}.')}</p>
    </>
  );
}
