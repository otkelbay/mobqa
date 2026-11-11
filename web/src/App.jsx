import React from 'react';
import { BrowserRouter } from 'react-router-dom';
import { AppDataProvider } from './context/AppDataContext.jsx';
import { ToastContainer } from './components/ui/ToastContainer.jsx';
import { ToastContext, useToastState } from './hooks/useToasts.js';
import { AppRouter } from './router/index.jsx';

function App() {
  const { toasts, addToast, dismissToast } = useToastState();
  return (
    <BrowserRouter>
      <AppDataProvider>
        <ToastContext.Provider value={addToast}>
          <AppRouter />
          <ToastContainer toasts={toasts} onDismiss={dismissToast} />
        </ToastContext.Provider>
      </AppDataProvider>
    </BrowserRouter>
  );
}

export { App };
