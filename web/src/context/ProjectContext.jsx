import React from 'react';
import {
  actionApi,
  buildApi,
  deviceApi,
  environmentApi,
  projectApi,
  runApi,
  testApi,
} from '../services/apiRepository.js';

const ProjectContext = React.createContext(null);

const ROLE_RANK = { viewer: 0, engineer: 1, admin: 2, owner: 3 };

const LOADERS = {
  project: projectApi.get,
  tests: testApi.list,
  runs: runApi.list,
  builds: buildApi.list,
  devices: deviceApi.list,
  environments: environmentApi.list,
  actions: actionApi.list,
};

const EMPTY = {
  project: null,
  tests: [],
  runs: [],
  builds: [],
  devices: [],
  environments: [],
  actions: [],
};

// ProjectProvider loads one project's shared collections. Pages that change a collection call reload with
// its name; data only one page needs (knowledge, hubs, versions, authorings) stays in that page.
function ProjectProvider({ projectId, children }) {
  const [data, setData] = React.useState(EMPTY);
  const [error, setError] = React.useState(null);

  const reload = React.useCallback(
    async (...names) => {
      const wanted = names.length ? names : Object.keys(LOADERS);
      try {
        const loaded = await Promise.all(wanted.map((name) => LOADERS[name](projectId)));
        setData((prev) => ({ ...prev, ...Object.fromEntries(wanted.map((name, i) => [name, loaded[i]])) }));
        setError(null);
      } catch (err) {
        setError(err);
      }
    },
    [projectId],
  );

  React.useEffect(() => {
    setData(EMPTY);
    reload();
  }, [reload]);

  const can = React.useCallback(
    (role) => Boolean(data.project) && ROLE_RANK[data.project.role] >= ROLE_RANK[role],
    [data.project],
  );

  const value = React.useMemo(
    () => ({ projectId, ...data, error, reload, can }),
    [projectId, data, error, reload, can],
  );

  return <ProjectContext.Provider value={value}>{children}</ProjectContext.Provider>;
}

function useProject() {
  const value = React.useContext(ProjectContext);
  if (!value) {
    throw new Error('useProject must be used inside ProjectProvider');
  }
  return value;
}

export { ProjectProvider, useProject };
