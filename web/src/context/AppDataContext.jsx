import React from 'react';
import { authApi, projectApi, userApi } from '../services/apiRepository.js';
import { onSessionExpired } from '../services/apiClient.js';
import { readSession } from '../services/authSession.js';

const AppDataContext = React.createContext(null);

// AppDataProvider holds what belongs to the signed-in person rather than to one project: the session,
// the profile and the projects they are a member of.
function AppDataProvider({ children }) {
  const [session, setSession] = React.useState(readSession);
  const [user, setUser] = React.useState(session?.user || null);
  const [projects, setProjects] = React.useState([]);
  const [error, setError] = React.useState(null);

  const signedIn = Boolean(session?.accessToken);

  const reloadProjects = React.useCallback(async () => {
    try {
      setProjects(await projectApi.list());
      setError(null);
    } catch (err) {
      setError(err);
    }
  }, []);

  React.useEffect(() => {
    if (!signedIn) {
      setProjects([]);
      setUser(null);
      return;
    }
    reloadProjects();
    userApi.profile().then(setUser, setError);
  }, [signedIn, reloadProjects]);

  React.useEffect(() => onSessionExpired(() => setSession(null)), []);

  const login = React.useCallback(async (email, password) => {
    const next = await authApi.login(email, password);
    setSession(next);
    setUser(next.user);
  }, []);

  const register = React.useCallback(async (details) => {
    const next = await authApi.register(details);
    setSession(next);
    setUser(next.user);
  }, []);

  const logout = React.useCallback(async () => {
    await authApi.logout();
    setSession(null);
  }, []);

  const updateProfile = React.useCallback(async (changes) => {
    setUser(await userApi.updateProfile(changes));
  }, []);

  const value = React.useMemo(
    () => ({ signedIn, user, projects, error, login, register, logout, updateProfile, reloadProjects }),
    [signedIn, user, projects, error, login, register, logout, updateProfile, reloadProjects],
  );

  return <AppDataContext.Provider value={value}>{children}</AppDataContext.Provider>;
}

function useAppData() {
  const value = React.useContext(AppDataContext);
  if (!value) {
    throw new Error('useAppData must be used inside AppDataProvider');
  }
  return value;
}

export { AppDataProvider, useAppData };
