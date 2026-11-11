const AUTH_SESSION_KEY = 'mobqa.auth.session';

function readSession() {
  if (typeof window === 'undefined') return null;
  const raw = window.localStorage.getItem(AUTH_SESSION_KEY);
  if (!raw) return null;
  try {
    return JSON.parse(raw);
  } catch {
    window.localStorage.removeItem(AUTH_SESSION_KEY);
    return null;
  }
}

function saveSession(authResponse) {
  const session = {
    accessToken: authResponse.access_token,
    refreshToken: authResponse.refresh_token,
    user: authResponse.user,
  };
  if (typeof window !== 'undefined') {
    window.localStorage.setItem(AUTH_SESSION_KEY, JSON.stringify(session));
  }
  return session;
}

function clearSession() {
  if (typeof window !== 'undefined') {
    window.localStorage.removeItem(AUTH_SESSION_KEY);
  }
}

function getAccessToken() {
  return readSession()?.accessToken || '';
}

function getRefreshToken() {
  return readSession()?.refreshToken || '';
}

export { clearSession, getAccessToken, getRefreshToken, readSession, saveSession };
