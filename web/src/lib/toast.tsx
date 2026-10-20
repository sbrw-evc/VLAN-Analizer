import { useSyncExternalStore } from 'react';
import { CircleCheck, CircleX, Info, X } from 'lucide-react';
import { AnimatePresence, motion } from 'motion/react';
import { t } from '../i18n';
import { describeError } from '../api/client';

export interface Toast {
  id: number;
  kind: 'info' | 'error' | 'success';
  text: string;
}

let toasts: Toast[] = [];
let nextId = 1;
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

export function toast(text: string, kind: Toast['kind'] = 'success', ttl = 4000) {
  const item = { id: nextId++, kind, text };
  toasts = [...toasts, item].slice(-5);
  emit();
  setTimeout(() => dismissToast(item.id), ttl);
}

export function toastError(err: unknown) {
  toast(describeError(err), 'error', 7000);
}

export function dismissToast(id: number) {
  toasts = toasts.filter((x) => x.id !== id);
  emit();
}

const ICONS = { info: Info, error: CircleX, success: CircleCheck };
const TONE = { info: 'info', error: 'error', success: 'ok' };

/** Notifications in the bottom right corner (Beacon Toaster). */
export function Toasts() {
  const list = useSyncExternalStore(
    (cb) => {
      listeners.add(cb);
      return () => listeners.delete(cb);
    },
    () => toasts,
  );
  return (
    <div className="toaster" role="status" aria-live="polite">
      <AnimatePresence initial={false}>
        {list.map((item) => {
          const Icon = ICONS[item.kind];
          return (
            <motion.div
              key={item.id}
              layout
              className={`toast toast-${TONE[item.kind]}`}
              initial={{ opacity: 0, y: 16, scale: 0.96 }}
              animate={{ opacity: 1, y: 0, scale: 1 }}
              exit={{ opacity: 0, x: 40, transition: { duration: 0.18 } }}
              transition={{ type: 'spring', stiffness: 420, damping: 32 }}
            >
              <Icon className="toast-icon" size={18} aria-hidden />
              <div className="toast-text">
                <div className="toast-title">{item.text}</div>
              </div>
              <button type="button" className="icon-btn toast-close" onClick={() => dismissToast(item.id)} aria-label={t('Close')}>
                <X size={16} />
              </button>
            </motion.div>
          );
        })}
      </AnimatePresence>
    </div>
  );
}
