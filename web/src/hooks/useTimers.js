import React from 'react';

function useTimers() {
  const timeouts = React.useRef(new Set());
  const intervals = React.useRef(new Set());

  const clearSafeTimeout = React.useCallback((id) => {
    clearTimeout(id);
    timeouts.current.delete(id);
  }, []);

  const clearSafeInterval = React.useCallback((id) => {
    clearInterval(id);
    intervals.current.delete(id);
  }, []);

  const setSafeTimeout = React.useCallback((callback, delay) => {
    const id = setTimeout(() => {
      timeouts.current.delete(id);
      callback();
    }, delay);
    timeouts.current.add(id);
    return id;
  }, []);

  const setSafeInterval = React.useCallback((callback, delay) => {
    const id = setInterval(callback, delay);
    intervals.current.add(id);
    return id;
  }, []);

  React.useEffect(
    () => () => {
      timeouts.current.forEach(clearTimeout);
      intervals.current.forEach(clearInterval);
      timeouts.current.clear();
      intervals.current.clear();
    },
    [],
  );

  return { clearSafeInterval, clearSafeTimeout, setSafeInterval, setSafeTimeout };
}

export { useTimers };
