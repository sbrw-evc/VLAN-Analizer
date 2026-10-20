import { useMemo, useRef, useState } from 'react';
import { motion, useReducedMotion } from 'motion/react';
import type { CheckResult } from '../api/types';
import { formatTime, useT } from '../i18n';

const W = 600;
const H = 180;
const PAD = { top: 12, right: 12, bottom: 24, left: 48 };

function niceMax(v: number) {
  if (v <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p;
  return 10 * p;
}

/** HTTP total time over the last day: one series, a crosshair with a tooltip, failed checks as red dots on the baseline. */
export function HistoryChart({ history }: { history: CheckResult[] }) {
  const t = useT();
  const reduce = useReducedMotion();
  const box = useRef<SVGSVGElement>(null);
  const [hover, setHover] = useState<number | null>(null);
  const rows = useMemo(() => [...history].sort((a, b) => a.at.localeCompare(b.at)), [history]);
  const geo = useMemo(() => {
    if (rows.length === 0) return null;
    const times = rows.map((r) => new Date(r.at).getTime());
    const from = times[0];
    const span = Math.max(1, times[times.length - 1] - from);
    const max = niceMax(Math.max(0, ...rows.map((r) => r.http?.total_ms ?? 0)));
    const x = (ms: number) => PAD.left + ((ms - from) / span) * (W - PAD.left - PAD.right);
    const y = (v: number) => H - PAD.bottom - (v / max) * (H - PAD.top - PAD.bottom);
    const points = rows.map((r, i) => ({ r, x: x(times[i]), y: r.http?.total_ms != null ? y(r.http.total_ms) : null }));
    let line = '';
    let pen = false;
    for (const p of points) {
      if (p.y == null) {
        pen = false;
        continue;
      }
      line += `${pen ? 'L' : 'M'}${p.x.toFixed(1)},${p.y.toFixed(1)}`;
      pen = true;
    }
    const drawn = points.filter((p) => p.y != null);
    const area = drawn.length > 1 ? `M${drawn[0].x},${H - PAD.bottom}` + drawn.map((p) => `L${p.x.toFixed(1)},${p.y!.toFixed(1)}`).join('') + `L${drawn[drawn.length - 1].x},${H - PAD.bottom}Z` : '';
    return { points, line, area, max, ticks: [0, max / 2, max] };
  }, [rows]);

  if (!geo || !geo.line) return <div className="chart-empty">{t('No HTTP timing samples in the last 24 hours')}</div>;

  const onMove = (e: React.PointerEvent) => {
    const r = box.current!.getBoundingClientRect();
    const px = ((e.clientX - r.left) / r.width) * W;
    let best = 0;
    geo.points.forEach((p, i) => {
      if (Math.abs(p.x - px) < Math.abs(geo.points[best].x - px)) best = i;
    });
    setHover(best);
  };
  const h = hover != null ? geo.points[hover] : null;

  return (
    <div className="chart" onPointerLeave={() => setHover(null)}>
      <svg ref={box} viewBox={`0 0 ${W} ${H}`} role="img" aria-label={t('HTTP response time, last 24 hours')} onPointerMove={onMove}>
        <g className="chart-grid">
          {geo.ticks.map((v) => {
            const y = H - PAD.bottom - (v / geo.max) * (H - PAD.top - PAD.bottom);
            return <line key={v} x1={PAD.left} x2={W - PAD.right} y1={y} y2={y} />;
          })}
        </g>
        <g className="chart-axis">
          {geo.ticks.map((v) => (
            <text key={v} x={PAD.left - 8} y={H - PAD.bottom - (v / geo.max) * (H - PAD.top - PAD.bottom) + 4} textAnchor="end">
              {Math.round(v)} {t('ms')}
            </text>
          ))}
          <text x={PAD.left} y={H - 6}>
            {formatTime(rows[0].at)}
          </text>
          <text x={W - PAD.right} y={H - 6} textAnchor="end">
            {formatTime(rows[rows.length - 1].at)}
          </text>
        </g>
        {geo.area && <motion.path className="chart-area" d={geo.area} initial={{ opacity: 0 }} animate={{ opacity: 1 }} transition={{ duration: 0.5, delay: 0.3 }} />}
        <motion.path className="chart-line" d={geo.line} initial={reduce ? false : { pathLength: 0 }} animate={{ pathLength: 1 }} transition={{ duration: 0.9, ease: [0.22, 1, 0.36, 1] }} />
        {geo.points
          .filter((p) => p.r.status === 'down' || p.r.status === 'error')
          .map((p) => (
            <circle key={p.r.at} className="chart-fail" cx={p.x} cy={H - PAD.bottom} r={4} />
          ))}
        {h && (
          <g>
            <line className="chart-cross" x1={h.x} x2={h.x} y1={PAD.top} y2={H - PAD.bottom} />
            {h.y != null && <circle className="chart-point" cx={h.x} cy={h.y} r={5} />}
          </g>
        )}
        <rect x={PAD.left} y={0} width={W - PAD.left - PAD.right} height={H} fill="transparent" />
      </svg>
      {h && (
        <div className="chart-tip" style={{ left: `${(h.x / W) * 100}%` }}>
          <strong>{h.r.http?.total_ms != null ? `${Math.round(h.r.http.total_ms)} ${t('ms')}` : '—'}</strong>
          <span className="muted">
            {formatTime(h.r.at)} · {h.r.http ? `HTTP ${h.r.http.code}` : h.r.status === 'up' ? t('Healthy') : t('No response')}
          </span>
        </div>
      )}
    </div>
  );
}
