import { clearSession, getAccessToken, getRefreshToken, saveSession } from './authSession.js';

// Without VITE_API_BASE_URL the API is expected behind the same origin at /api,
// never at the viewer's own localhost.
const API_BASE_URL = (import.meta.env.VITE_API_BASE_URL || `${window.location.origin}/api`).replace(/\/$/, '');

class ApiError extends Error {
  constructor(message, status, payload) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.payload = payload;
  }
}

const sessionExpiredListeners = new Set();

function onSessionExpired(listener) {
  sessionExpiredListeners.add(listener);
  return () => sessionExpiredListeners.delete(listener);
}

function invalidateSession() {
  clearSession();
  sessionExpiredListeners.forEach((listener) => listener());
}

async function parseResponse(response) {
  if (response.status === 204) return null;
  const text = await response.text();
  if (!text) return null;
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

async function refreshAccessToken() {
  const refreshToken = getRefreshToken();
  if (!refreshToken) return null;

  const response = await fetch(`${API_BASE_URL}/auth/refresh`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ refresh_token: refreshToken }),
  });
  const payload = await parseResponse(response);
  if (!response.ok) {
    invalidateSession();
    return null;
  }
  return saveSession(payload);
}

function encodeBody(body) {
  if (body === undefined) return { payload: undefined, contentType: null };
  if (typeof body === 'string') return { payload: body, contentType: null };
  if (typeof FormData !== 'undefined' && body instanceof FormData) return { payload: body, contentType: null };
  return { payload: JSON.stringify(body), contentType: 'application/json' };
}

// apiFetch sends an authorized request, refreshing the access token once on 401, and throws ApiError for
// any failure. It returns the raw response for callers that read blobs or SDP.
async function apiFetch(path, options = {}) {
  const { auth = true, body, headers, retry = true, ...init } = options;
  const { payload, contentType } = encodeBody(body);
  const requestHeaders = { ...(contentType ? { 'Content-Type': contentType } : {}), ...headers };
  const token = getAccessToken();
  if (auth && token) {
    requestHeaders.Authorization = `Bearer ${token}`;
  }

  const response = await fetch(`${API_BASE_URL}${path}`, { ...init, headers: requestHeaders, body: payload });

  if (response.status === 401 && auth) {
    if (retry) {
      const session = await refreshAccessToken();
      if (session?.accessToken) {
        return apiFetch(path, { ...options, retry: false });
      }
    }
    invalidateSession();
  }
  if (!response.ok) {
    const parsed = await parseResponse(response);
    const message = typeof parsed === 'object' && parsed?.error ? parsed.error : 'API request failed';
    throw new ApiError(message, response.status, parsed);
  }
  return response;
}

async function apiRequest(path, options = {}) {
  return parseResponse(await apiFetch(path, options));
}

export { API_BASE_URL, ApiError, apiFetch, apiRequest, onSessionExpired };
