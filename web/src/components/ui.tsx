import { useEffect, useId, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { AlertTriangle, Eye, EyeOff, LoaderCircle, Moon, MoreHorizontal, Sun, X } from 'lucide-react';
import { AnimatePresence, animate, motion, useReducedMotion, type Transition } from 'motion/react';
import { describeError } from '../api/client';
import type { Status } from '../api/types';
import { setLang, useLang, useT, type Lang } from '../i18n';
import { originOf, setTheme, useTheme } from '../lib/theme';

/** The spring Beacon uses for pills, chevrons and dialogs. */
export const spring: Transition = { type: 'spring', stiffness: 420, damping: 30 };

export function Brand({ subtitle, compact }: { subtitle?: string; compact?: boolean }) {
  return (
    <span className="brand">
      <img src="/logo.svg" alt="" width={32} height={32} />
      {!compact && (
        <span>
          <span className="brand-name">VLAN Analizer</span>
          {subtitle && <span className="brand-sub"> · {subtitle}</span>}
        </span>
      )}
    </span>
  );
}

export function Segmented<T extends string>({ value, options, onChange, label }: { value: T; options: { value: T; label: ReactNode }[]; onChange: (v: T) => void; label: string }) {
  const group = useId();
  return (
    <div className="segmented has-pill" role="radiogroup" aria-label={label}>
      {options.map((o) => (
        <button key={o.value} type="button" role="radio" aria-checked={value === o.value} className={value === o.value ? 'active' : ''} onClick={() => onChange(o.value)}>
          {value === o.value && <motion.span layoutId={`seg-${group}`} className="seg-pill" transition={spring} />}
          <span className="seg-label">{o.label}</span>
        </button>
      ))}
    </div>
  );
}

export function ThemeToggle() {
  const t = useT();
  const theme = useTheme();
  return (
    <button type="button" className="icon-btn" aria-label={t('Switch theme')} title={t('Switch theme')} onClick={(e) => setTheme(theme === 'dark' ? 'light' : 'dark', originOf(e))}>
      <AnimatePresence mode="wait" initial={false}>
        <motion.span
          key={theme}
          style={{ display: 'inline-flex' }}
          initial={{ rotate: -90, scale: 0.4, opacity: 0 }}
          animate={{ rotate: 0, scale: 1, opacity: 1 }}
          exit={{ rotate: 90, scale: 0.4, opacity: 0 }}
          transition={{ duration: 0.22 }}
        >
          {theme === 'dark' ? <Sun size={18} /> : <Moon size={18} />}
        </motion.span>
      </AnimatePresence>
    </button>
  );
}

/** Language (EN / RU) and theme switches of the sign-in and setup pages. */
export function Preferences() {
  const t = useT();
  const lang = useLang();
  return (
    <div className="prefs">
      <Segmented<Lang>
        label={t('Language')}
        value={lang}
        onChange={setLang}
        options={[
          { value: 'en', label: 'EN' },
          { value: 'ru', label: 'RU' },
        ]}
      />
      <ThemeToggle />
    </div>
  );
}

export function PageHead({ title, subtitle, actions, icon }: { title: ReactNode; subtitle?: ReactNode; actions?: ReactNode; icon?: ReactNode }) {
  return (
    <div className="page-head">
      <div>
        <h1>
          {icon}
          {title}
        </h1>
        {subtitle && <p className="muted">{subtitle}</p>}
      </div>
      {actions && <div className="page-head-actions">{actions}</div>}
    </div>
  );
}

function useEscape(onClose: () => void) {
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeRef.current();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
}

export function Modal({ title, onClose, children, footer, width = 520, testId }: { title?: ReactNode; onClose: () => void; children: ReactNode; footer?: ReactNode; width?: number | string; testId?: string }) {
  const t = useT();
  useEscape(onClose);
  return createPortal(
    <motion.div
      className="modal-backdrop"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      transition={{ duration: 0.16 }}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <motion.div className="modal" style={{ width }} role="dialog" aria-modal="true" data-testid={testId} initial={{ opacity: 0, y: 16, scale: 0.97 }} animate={{ opacity: 1, y: 0, scale: 1 }} exit={{ opacity: 0, y: 10, scale: 0.98 }} transition={spring}>
        {title !== undefined && (
          <div className="modal-header">
            <div className="modal-title">{title}</div>
            <button type="button" className="icon-btn" onClick={onClose} aria-label={t('Close')}>
              <X size={18} />
            </button>
          </div>
        )}
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-footer">{footer}</div>}
      </motion.div>
    </motion.div>,
    document.body,
  );
}

/** A side panel that slides in from the right (Ledger's record drawer). */
export function Drawer({ onClose, header, children, testId }: { onClose: () => void; header: ReactNode; children: ReactNode; testId?: string }) {
  const t = useT();
  const reduce = useReducedMotion();
  useEscape(onClose);
  return createPortal(
    <>
      <motion.div className="drawer-backdrop" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.18 }} onMouseDown={onClose} />
      <motion.aside
        className="drawer"
        role="dialog"
        aria-modal="true"
        data-testid={testId}
        initial={reduce ? { opacity: 0 } : { x: 48, opacity: 0 }}
        animate={{ x: 0, opacity: 1 }}
        exit={reduce ? { opacity: 0 } : { x: 48, opacity: 0 }}
        transition={spring}
      >
        <div className="drawer-head">
          <div>{header}</div>
          <button type="button" className="icon-btn" onClick={onClose} aria-label={t('Close')}>
            <X size={18} />
          </button>
        </div>
        <div className="drawer-body">{children}</div>
      </motion.aside>
    </>,
    document.body,
  );
}

/** A confirmation dialog; destructive ones say that the action cannot be undone. */
export function Confirm({ title, text, confirm, danger, onConfirm, onClose }: { title: string; text: ReactNode; confirm: string; danger?: boolean; onConfirm: () => Promise<unknown> | void; onClose: () => void }) {
  const t = useT();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  return (
    <Modal
      title={title}
      onClose={onClose}
      width={460}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {t('Cancel')}
          </button>
          <button
            type="button"
            className={`btn ${danger ? 'btn-danger' : 'btn-primary'}`}
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              try {
                await onConfirm();
                onClose();
              } catch (e) {
                setError(e);
                setBusy(false);
              }
            }}
          >
            {busy && <LoaderCircle size={16} className="spin" />}
            {confirm}
          </button>
        </>
      }
    >
      <div className={`confirm-text ${danger ? 'confirm-danger' : ''}`}>
        {danger && <AlertTriangle size={22} />}
        <div className="stack" style={{ gap: 6 }}>
          <p>{text}</p>
          {danger && <p className="muted">{t('This cannot be undone.')}</p>}
        </div>
      </div>
      <ErrorNotice error={error} />
    </Modal>
  );
}

export function Switch({ checked, onChange, label, hint, disabled, name }: { checked: boolean; onChange: (v: boolean) => void; label: ReactNode; hint?: ReactNode; disabled?: boolean; name?: string }) {
  return (
    <label className="switch-row">
      <span className="switch">
        <input type="checkbox" role="switch" name={name} checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
        <span className="switch-track" />
      </span>
      <span>
        <strong style={{ fontWeight: 600 }}>{label}</strong>
        {hint && <span className="hint">{hint}</span>}
      </span>
    </label>
  );
}

export function Field({ label, hint, children }: { label: ReactNode; hint?: ReactNode; children: ReactNode }) {
  return (
    <div className="field">
      <label className="field">
        <span className="field-label" style={{ margin: 0 }}>
          {label}
        </span>
        {children}
      </label>
      {hint && <span className="hint">{hint}</span>}
    </div>
  );
}

export function PasswordInput({ value, onChange, autoComplete, autoFocus, name, placeholder }: { value: string; onChange: (v: string) => void; autoComplete?: string; autoFocus?: boolean; name?: string; placeholder?: string }) {
  const t = useT();
  const [show, setShow] = useState(false);
  return (
    <div className="password">
      <input name={name} className="input" type={show ? 'text' : 'password'} value={value} onChange={(e) => onChange(e.target.value)} autoComplete={autoComplete} autoFocus={autoFocus} placeholder={placeholder} maxLength={1024} />
      <button type="button" className="icon-btn" onClick={() => setShow((v) => !v)} aria-label={show ? t('Hide password') : t('Show password')} title={show ? t('Hide password') : t('Show password')}>
        {show ? <EyeOff size={16} /> : <Eye size={16} />}
      </button>
    </div>
  );
}

const AVATAR_COLORS = ['#65a30d', '#ca8a04', '#0d9488', '#2563eb', '#7c3aed', '#db2777', '#ea580c', '#16a34a'];

export function initialsOf(name: string) {
  const parts = name.trim().split(/[\s@._-]+/).filter(Boolean);
  return ((parts[0]?.[0] ?? '?') + (parts[1]?.[0] ?? '')).toUpperCase();
}

export function Avatar({ name, size = 32 }: { name: string; size?: number }) {
  let h = 0;
  for (const ch of name) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return (
    <span className="avatar" style={{ width: size, height: size, fontSize: Math.round(size * 0.38), background: AVATAR_COLORS[h % AVATAR_COLORS.length] }} aria-hidden>
      {initialsOf(name)}
    </span>
  );
}

export function Loading() {
  const t = useT();
  return (
    <div className="view-loading">
      <LoaderCircle size={20} className="spin" /> {t('Loading…')}
    </div>
  );
}

export function ErrorNotice({ error }: { error: unknown }) {
  return (
    <AnimatePresence initial={false}>
      {!!error && (
        <motion.div className="notice notice-error" role="alert" initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: 'auto' }} exit={{ opacity: 0, height: 0 }} transition={{ duration: 0.18 }}>
          {describeError(error)}
        </motion.div>
      )}
    </AnimatePresence>
  );
}

export function Empty({ icon, title, text, action }: { icon: ReactNode; title: string; text?: ReactNode; action?: ReactNode }) {
  return (
    <div className="empty-state">
      {icon}
      <h3>{title}</h3>
      {text && <p>{text}</p>}
      {action}
    </div>
  );
}

/** A drop-down menu of actions next to a button, in a portal so tables do not clip it. It opens upwards when the
 *  space below the button is short, follows the button while the page scrolls and closes when the button leaves. */
type MenuPos = { right: number; top?: number; bottom?: number };
const MENU_ROOM = 220;

function menuPos(r: DOMRect): MenuPos {
  const right = window.innerWidth - r.right;
  return window.innerHeight - r.bottom < MENU_ROOM && r.top > MENU_ROOM ? { right, bottom: window.innerHeight - r.top + 6 } : { right, top: r.bottom + 6 };
}

export function ActionMenu({ label, children }: { label: string; children: (close: () => void) => ReactNode }) {
  const [pos, setPos] = useState<MenuPos | null>(null);
  const btn = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const open = !!pos;
  useEffect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent) => {
      if (!menu.current?.contains(e.target as Node) && !btn.current?.contains(e.target as Node)) setPos(null);
    };
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && setPos(null);
    const follow = () => {
      const r = btn.current?.getBoundingClientRect();
      setPos(!r || r.bottom < 0 || r.top > window.innerHeight ? null : menuPos(r));
    };
    document.addEventListener('pointerdown', onDown);
    document.addEventListener('keydown', onKey);
    window.addEventListener('resize', follow);
    document.addEventListener('scroll', follow, true);
    return () => {
      document.removeEventListener('pointerdown', onDown);
      document.removeEventListener('keydown', onKey);
      window.removeEventListener('resize', follow);
      document.removeEventListener('scroll', follow, true);
    };
  }, [open]);
  const up = pos?.bottom !== undefined;
  return (
    <>
      <button
        ref={btn}
        type="button"
        className="icon-btn"
        aria-label={label}
        title={label}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setPos(pos ? null : menuPos(btn.current!.getBoundingClientRect()))}
      >
        <MoreHorizontal size={18} />
      </button>
      {createPortal(
        <AnimatePresence>
          {pos && (
            <motion.div
              ref={menu}
              role="menu"
              className="user-dropdown"
              style={{ position: 'fixed', top: pos.top ?? 'auto', bottom: pos.bottom ?? 'auto', right: pos.right, width: 240, transformOrigin: up ? 'bottom right' : 'top right' }}
              initial={{ opacity: 0, y: up ? 6 : -6, scale: 0.97 }}
              animate={{ opacity: 1, y: 0, scale: 1 }}
              exit={{ opacity: 0, y: up ? 4 : -4, scale: 0.98 }}
              transition={{ duration: 0.14 }}
            >
              {children(() => setPos(null))}
            </motion.div>
          )}
        </AnimatePresence>,
        document.body,
      )}
    </>
  );
}

/** A number that counts to its new value. */
export function AnimatedNumber({ value }: { value: number }) {
  const ref = useRef<HTMLSpanElement>(null);
  const last = useRef(0);
  const reduce = useReducedMotion();
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (reduce) {
      el.textContent = String(value);
      last.current = value;
      return;
    }
    const controls = animate(last.current, value, { duration: 0.6, ease: [0.22, 1, 0.36, 1], onUpdate: (v) => (el.textContent = String(Math.round(v))) });
    last.current = value;
    return () => controls.stop();
  }, [value, reduce]);
  return <span ref={ref}>{value}</span>;
}

export function Tile({ icon, label, value, foot, tone }: { icon: ReactNode; label: string; value: number; foot?: ReactNode; tone?: 'ok' | 'warn' | 'error' | 'info' }) {
  return (
    <div className={`tile ${tone ? `tone-${tone}` : ''}`}>
      <span className="tile-icon">{icon}</span>
      <span className="tile-label">{label}</span>
      <span className="tile-value">
        <AnimatedNumber value={value} />
      </span>
      {foot && <span className="tile-foot">{foot}</span>}
    </div>
  );
}

export function statusLabel(t: (k: string) => string, s: Status | 'paused') {
  return { up: t('Healthy'), degraded: t('Degraded'), down: t('Down'), error: t('Error'), unknown: t('Unknown'), paused: t('Paused') }[s] ?? s;
}

export function StatusChip({ status, onClick, children }: { status: Status | 'paused'; onClick?: () => void; children?: ReactNode }) {
  const t = useT();
  const body = (
    <>
      <span className="status-dot" aria-hidden />
      {children ?? statusLabel(t, status)}
    </>
  );
  return onClick ? (
    <button type="button" className={`status-chip status-${status}`} onClick={onClick}>
      {body}
    </button>
  ) : (
    <span className={`status-chip status-${status}`}>{body}</span>
  );
}

/** The Microsoft mark for the Entra sign-in button (a simple drawing, no brand asset). */
export function EntraMark() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden>
      <rect x="2" y="2" width="9.5" height="9.5" fill="#f25022" />
      <rect x="12.5" y="2" width="9.5" height="9.5" fill="#7fba00" />
      <rect x="2" y="12.5" width="9.5" height="9.5" fill="#00a4ef" />
      <rect x="12.5" y="12.5" width="9.5" height="9.5" fill="#ffb900" />
    </svg>
  );
}
