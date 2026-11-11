import React from 'react';
import { useTimers } from './useTimers.js';

const ToastContext = React.createContext(() => {});

const TOAST_MS = 5000;

function useToastState() {
  const [toasts, setToasts] = React.useState([]);
  const { setSafeTimeout } = useTimers();

  const addToast = React.useCallback(
    (toast) => {
      const id = `${Date.now()}-${Math.random()}`;
      setToasts((prev) => [...prev, { ...toast, id }]);
      setSafeTimeout(() => setToasts((prev) => prev.filter((x) => x.id !== id)), TOAST_MS);
    },
    [setSafeTimeout],
  );

  const dismissToast = React.useCallback((id) => {
    setToasts((prev) => prev.filter((x) => x.id !== id));
  }, []);

  return { toasts, addToast, dismissToast };
}

// useToast returns addToast({ title, body, type: 'success' | 'warning' | 'error' }).
function useToast() {
  return React.useContext(ToastContext);
}

export { ToastContext, useToast, useToastState };
