import React from 'react';
import { Navigate, Outlet, Route, Routes, useLocation, useParams } from 'react-router-dom';
import { useAppData } from '../context/AppDataContext.jsx';
import { ProjectProvider } from '../context/ProjectContext.jsx';
import { AppShell } from '../layouts/AppShell.jsx';
import { AuthPage } from '../pages/auth/AuthPage.jsx';
import { DashboardPage } from '../pages/apps/DashboardPage.jsx';
import { NewAppPage } from '../pages/apps/NewAppPage.jsx';
import { BuildsPage } from '../pages/apps/BuildsPage.jsx';
import { TestsPage } from '../pages/tests/TestsPage.jsx';
import { NewTestChoicePage } from '../pages/tests/NewTestChoicePage.jsx';
import { NewTestPage } from '../pages/tests/NewTestPage.jsx';
import { ManualTestPage } from '../pages/tests/ManualTestPage.jsx';
import { GeneratingPage } from '../pages/tests/GeneratingPage.jsx';
import { TestDetailPage } from '../pages/tests/TestDetailPage.jsx';
import { AuthoringPage } from '../pages/tests/AuthoringPage.jsx';
import { RunsPage } from '../pages/runs/RunsPage.jsx';
import { RunDetailPage } from '../pages/runs/RunDetailPage.jsx';
import { DevicesPage } from '../pages/devices/DevicesPage.jsx';
import { DeviceControlPage } from '../pages/devices/DeviceControlPage.jsx';
import { KnowledgePage } from '../pages/knowledge/KnowledgePage.jsx';
import { ApiActionsPage } from '../pages/resources/ApiActionsPage.jsx';
import { EnvironmentsPage } from '../pages/resources/EnvironmentsPage.jsx';
import { ProjectSettingsPage } from '../pages/settings/ProjectSettingsPage.jsx';
import { ProfilePage } from '../pages/settings/ProfilePage.jsx';

const REDIRECT_KEY = 'mobqa.redirectAfterLogin';

function RequireAuth() {
  const { signedIn } = useAppData();
  const location = useLocation();
  if (!signedIn) {
    if (location.pathname !== '/') {
      sessionStorage.setItem(REDIRECT_KEY, location.pathname + location.search);
    }
    return <Navigate to="/auth" replace />;
  }
  return <Outlet />;
}

function PublicOnly() {
  const { signedIn } = useAppData();
  if (signedIn) {
    const redirect = sessionStorage.getItem(REDIRECT_KEY);
    sessionStorage.removeItem(REDIRECT_KEY);
    return <Navigate to={redirect || '/'} replace />;
  }
  return <Outlet />;
}

function ProjectLayout() {
  const { appId } = useParams();
  return (
    <ProjectProvider key={appId} projectId={appId}>
      <AppShell>
        <Outlet />
      </AppShell>
    </ProjectProvider>
  );
}

function AppRouter() {
  return (
    <Routes>
      <Route element={<PublicOnly />}>
        <Route path="/auth" element={<AuthPage />} />
      </Route>
      <Route element={<RequireAuth />}>
        <Route path="/" element={<DashboardPage />} />
        <Route path="/apps/new" element={<NewAppPage />} />
        <Route path="/profile" element={<ProfilePage />} />
        <Route path="/apps/:appId" element={<ProjectLayout />}>
          <Route index element={<Navigate to="tests" replace />} />
          <Route path="tests" element={<TestsPage />} />
          <Route path="tests/new" element={<NewTestChoicePage />} />
          <Route path="tests/new/agent" element={<NewTestPage />} />
          <Route path="tests/new/manual" element={<ManualTestPage />} />
          <Route path="tests/generations/:generationId" element={<GeneratingPage />} />
          <Route path="tests/:testId" element={<TestDetailPage />} />
          <Route path="authorings/:authoringId" element={<AuthoringPage />} />
          <Route path="runs" element={<RunsPage />} />
          <Route path="runs/:runId" element={<RunDetailPage />} />
          <Route path="builds" element={<BuildsPage />} />
          <Route path="devices" element={<DevicesPage />} />
          <Route path="devices/:deviceId" element={<DeviceControlPage />} />
          <Route path="knowledge" element={<KnowledgePage />} />
          <Route path="actions" element={<ApiActionsPage />} />
          <Route path="environments" element={<EnvironmentsPage />} />
          <Route path="settings" element={<ProjectSettingsPage />} />
        </Route>
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}

export { AppRouter };
