import { apiFetch, apiRequest } from './apiClient.js';
import { clearSession, getRefreshToken, saveSession } from './authSession.js';
import { stepFromApi, stepToApi } from './testStepModel.js';
import {
  mapAction,
  mapAttempt,
  mapAuthProfile,
  mapAuthoring,
  mapBuild,
  mapDevice,
  mapEnvironment,
  mapHub,
  mapNote,
  mapProject,
  mapProjectDetail,
  mapResult,
  mapRun,
  mapTest,
  mapTestSummary,
  mapVersion,
} from './viewModelMappers.js';

const PAGE = 'limit=100';

function project(projectId, path = '') {
  return `/projects/${encodeURIComponent(projectId)}${path}`;
}

function query(params) {
  const search = new URLSearchParams(Object.entries(params).filter(([, value]) => value));
  const text = search.toString();
  return text ? `?${text}` : '';
}

const authApi = {
  async login(email, password) {
    return saveSession(await apiRequest('/auth/login', { auth: false, method: 'POST', body: { email, password } }));
  },

  async register({ name, email, password }) {
    const session = await apiRequest('/auth/register', {
      auth: false,
      method: 'POST',
      body: { name, email, password },
    });
    return saveSession(session);
  },

  async logout() {
    const refreshToken = getRefreshToken();
    if (refreshToken) {
      await apiRequest('/auth/logout', { method: 'POST', body: { refresh_token: refreshToken } }).catch(() => null);
    }
    clearSession();
  },
};

const userApi = {
  profile: () => apiRequest('/user/profile'),
  updateProfile: (changes) => apiRequest('/user/profile', { method: 'PATCH', body: changes }),
};

function authProfileBody(profile) {
  return {
    method: profile.method,
    login_var: profile.loginVar,
    password_var: profile.passwordVar,
    otp:
      profile.otpOverride || profile.otpActionId
        ? { override: profile.otpOverride, action_id: profile.otpActionId }
        : undefined,
    sso: profile.method.endsWith('_sso')
      ? { provider: profile.method.replace('_sso', ''), account_var: profile.ssoAccountVar }
      : undefined,
    notes: profile.notes,
  };
}

const projectApi = {
  async list() {
    const page = await apiRequest(`/projects?${PAGE}`);
    return page.items.map(mapProject);
  },
  async get(projectId) {
    return mapProjectDetail(await apiRequest(project(projectId)));
  },
  async create({ name, description, platforms, packageName }) {
    const created = await apiRequest('/projects', {
      method: 'POST',
      body: { name, description, platforms, package_name: packageName },
    });
    return mapProject(created);
  },
  async update(projectId, { name, description, platforms, packageName, appearance }) {
    const body = { name, description, platforms, package_name: packageName };
    if (appearance) {
      body.appearance = { initial: appearance.initial, color: appearance.color, text_color: appearance.textColor };
    }
    return mapProject(await apiRequest(project(projectId), { method: 'PATCH', body }));
  },
  archive: (projectId) => apiRequest(project(projectId), { method: 'DELETE' }),
  async setAuthProfile(projectId, profile) {
    const saved = await apiRequest(project(projectId, '/auth-profile'), {
      method: 'PUT',
      body: authProfileBody(profile),
    });
    return mapAuthProfile(saved);
  },
  addMember: (projectId, { email, role }) =>
    apiRequest(project(projectId, '/members'), { method: 'POST', body: { email, role } }),
  setMemberRole: (projectId, userId, role) =>
    apiRequest(project(projectId, `/members/${userId}`), { method: 'PATCH', body: { role } }),
  removeMember: (projectId, userId) => apiRequest(project(projectId, `/members/${userId}`), { method: 'DELETE' }),
};

function variableBodies(variables) {
  return variables.map(({ name, type, label, rule, value }) => ({ name, type, label, rule, value }));
}

const environmentApi = {
  async list(projectId) {
    return (await apiRequest(project(projectId, '/environments'))).map(mapEnvironment);
  },
  async create(projectId, { name, isDefault, variables }) {
    const created = await apiRequest(project(projectId, '/environments'), {
      method: 'POST',
      body: { name, is_default: isDefault, variables: variableBodies(variables) },
    });
    return mapEnvironment(created);
  },
  async update(projectId, environmentId, { name, isDefault, variables }) {
    const updated = await apiRequest(project(projectId, `/environments/${environmentId}`), {
      method: 'PATCH',
      body: { name, is_default: isDefault, variables: variableBodies(variables) },
    });
    return mapEnvironment(updated);
  },
  remove: (projectId, environmentId) =>
    apiRequest(project(projectId, `/environments/${environmentId}`), { method: 'DELETE' }),
};

function actionBody({ name, description, request }) {
  return {
    name,
    description,
    request: {
      method: request.method,
      url: request.url,
      headers: request.headers,
      body: request.body,
      expected_status: Number(request.expectedStatus) || 0,
      extract: request.extract,
    },
  };
}

const actionApi = {
  async list(projectId) {
    return (await apiRequest(project(projectId, '/actions'))).map(mapAction);
  },
  async create(projectId, action) {
    return mapAction(await apiRequest(project(projectId, '/actions'), { method: 'POST', body: actionBody(action) }));
  },
  async replace(projectId, actionId, action) {
    const replaced = await apiRequest(project(projectId, `/actions/${actionId}`), {
      method: 'PUT',
      body: actionBody(action),
    });
    return mapAction(replaced);
  },
  remove: (projectId, actionId) => apiRequest(project(projectId, `/actions/${actionId}`), { method: 'DELETE' }),
};

const buildApi = {
  async list(projectId) {
    return (await apiRequest(project(projectId, `/builds?${PAGE}`))).items.map(mapBuild);
  },
  async upload(projectId, { file, platform, version, buildNumber }) {
    const body = new FormData();
    if (platform) body.append('platform', platform);
    if (version) body.append('version', version);
    if (buildNumber) body.append('build_number', buildNumber);
    body.append('file', file);
    return mapBuild(await apiRequest(project(projectId, '/builds'), { method: 'POST', body }));
  },
  setCurrent: (projectId, buildId) => apiRequest(project(projectId, `/builds/${buildId}/current`), { method: 'POST' }),
  remove: (projectId, buildId) => apiRequest(project(projectId, `/builds/${buildId}`), { method: 'DELETE' }),
  async download(projectId, buildId) {
    return (await apiFetch(project(projectId, `/builds/${buildId}/download`))).blob();
  },
};

function testBody({ title, description, platform, priority, tags, steps }) {
  return { title, description, platform, priority, tags, steps: steps.map(stepToApi) };
}

const testApi = {
  async list(projectId) {
    return (await apiRequest(project(projectId, `/tests?${PAGE}`))).items.map(mapTestSummary);
  },
  async get(projectId, testId) {
    return mapTest(await apiRequest(project(projectId, `/tests/${testId}`)));
  },
  async create(projectId, test) {
    return mapTest(await apiRequest(project(projectId, '/tests'), { method: 'POST', body: testBody(test) }));
  },
  // update sends only what changed; baseVersion makes a concurrent edit fail with 409 instead of being lost.
  async update(projectId, testId, { baseVersion, title, description, steps, priority, tags, note }) {
    const updated = await apiRequest(project(projectId, `/tests/${testId}`), {
      method: 'PATCH',
      body: { base_version: baseVersion, title, description, steps: steps?.map(stepToApi), priority, tags, note },
    });
    return mapTest(updated);
  },
  remove: (projectId, testId) => apiRequest(project(projectId, `/tests/${testId}`), { method: 'DELETE' }),
  async duplicate(projectId, testId) {
    return mapTest(await apiRequest(project(projectId, `/tests/${testId}/duplicate`), { method: 'POST' }));
  },
  async versions(projectId, testId) {
    return (await apiRequest(project(projectId, `/tests/${testId}/versions`))).map(mapVersion);
  },
  async version(projectId, testId, version) {
    return mapVersion(await apiRequest(project(projectId, `/tests/${testId}/versions/${version}`)));
  },
  async restore(projectId, testId, version) {
    const restored = await apiRequest(project(projectId, `/tests/${testId}/versions/${version}/restore`), {
      method: 'POST',
    });
    return mapTest(restored);
  },
  // agentEdit returns a proposal; nothing is saved until the steps are sent back with its base version.
  async agentEdit(projectId, testId, instruction, history) {
    const proposal = await apiRequest(project(projectId, `/tests/${testId}/agent-edit`), {
      method: 'POST',
      body: { instruction, history },
    });
    return {
      reply: proposal.reply,
      changed: proposal.changed,
      baseVersion: proposal.base_version,
      steps: (proposal.steps || []).map((step, index) => stepFromApi(step, `proposed-${index}`)),
    };
  },
  generate: (projectId, { platform, brief }) =>
    apiRequest(project(projectId, '/test-generations'), { method: 'POST', body: { platform, brief } }),
  generation: (projectId, generationId) => apiRequest(project(projectId, `/test-generations/${generationId}`)),
};

const authoringApi = {
  async list(projectId, testId) {
    return (await apiRequest(project(projectId, `/tests/${testId}/authorings`))).map(mapAuthoring);
  },
  async start(projectId, testId, { deviceId, environmentId }) {
    const started = await apiRequest(project(projectId, `/tests/${testId}/authorings`), {
      method: 'POST',
      body: { device_id: deviceId, environment_id: environmentId },
    });
    return mapAuthoring(started);
  },
  async get(projectId, authoringId) {
    return mapAuthoring(await apiRequest(project(projectId, `/authorings/${authoringId}`)));
  },
  cancel: (projectId, authoringId) =>
    apiRequest(project(projectId, `/authorings/${authoringId}/cancel`), { method: 'POST' }),
};

const knowledgeApi = {
  async list(projectId) {
    return (await apiRequest(project(projectId, `/knowledge?${PAGE}`))).items.map(mapNote);
  },
  async create(projectId, { kind, title, content, pinned }) {
    const created = await apiRequest(project(projectId, '/knowledge'), {
      method: 'POST',
      body: { kind, title, content, pinned },
    });
    return mapNote(created);
  },
  async update(projectId, noteId, changes) {
    return mapNote(await apiRequest(project(projectId, `/knowledge/${noteId}`), { method: 'PATCH', body: changes }));
  },
  remove: (projectId, noteId) => apiRequest(project(projectId, `/knowledge/${noteId}`), { method: 'DELETE' }),
  search: (projectId, text) => apiRequest(project(projectId, `/knowledge/search${query({ q: text, limit: 8 })}`)),
};

const runApi = {
  async list(projectId) {
    return (await apiRequest(project(projectId, `/runs?${PAGE}`))).items.map(mapRun);
  },
  async create(projectId, { testIds, deviceIds, buildId, environmentId, retries }) {
    const created = await apiRequest(project(projectId, '/runs'), {
      method: 'POST',
      body: { test_ids: testIds, device_ids: deviceIds, build_id: buildId, environment_id: environmentId, retries },
    });
    return mapRun(created);
  },
  async get(projectId, runId) {
    const run = await apiRequest(project(projectId, `/runs/${runId}`));
    return { run: mapRun(run), results: (run.results || []).map(mapResult) };
  },
  cancel: (projectId, runId) => apiRequest(project(projectId, `/runs/${runId}/cancel`), { method: 'POST' }),
  async attempts(projectId, runId, resultId) {
    return (await apiRequest(project(projectId, `/runs/${runId}/results/${resultId}/attempts`))).map(mapAttempt);
  },
  async video(projectId, runId, resultId, number) {
    const path = `/runs/${runId}/results/${resultId}/attempts/${number}/video`;
    return (await apiFetch(project(projectId, path))).blob();
  },
};

const hubApi = {
  async list(projectId) {
    return (await apiRequest(project(projectId, '/hubs'))).map(mapHub);
  },
  async register(projectId, name) {
    const registration = await apiRequest(project(projectId, '/hubs'), { method: 'POST', body: { name } });
    return { hub: mapHub(registration.hub), token: registration.token };
  },
  async rotateToken(projectId, hubId) {
    const registration = await apiRequest(project(projectId, `/hubs/${hubId}/token`), { method: 'POST' });
    return { hub: mapHub(registration.hub), token: registration.token };
  },
  remove: (projectId, hubId) => apiRequest(project(projectId, `/hubs/${hubId}`), { method: 'DELETE' }),
};

const deviceApi = {
  async list(projectId) {
    return (await apiRequest(project(projectId, '/devices'))).map(mapDevice);
  },
  async get(projectId, deviceId) {
    return mapDevice(await apiRequest(project(projectId, `/devices/${deviceId}`)));
  },
  async screen(projectId, deviceId) {
    return (await apiFetch(project(projectId, `/devices/${deviceId}/screen`))).blob();
  },
  command: (projectId, deviceId, command) =>
    apiRequest(project(projectId, `/devices/${deviceId}/commands`), { method: 'POST', body: command }),
  // watch sends the browser's SDP offer with all of its candidates and returns the hub's answer.
  async watch(projectId, deviceId, offer) {
    const response = await apiFetch(project(projectId, `/devices/${deviceId}/stream`), {
      method: 'POST',
      headers: { 'Content-Type': 'application/sdp' },
      body: offer,
    });
    return response.text();
  },
  async rotateStreamKey(projectId, deviceId) {
    const issued = await apiRequest(project(projectId, `/devices/${deviceId}/stream-key`), { method: 'POST' });
    return { device: mapDevice(issued.device), key: issued.key };
  },
  async iceServers() {
    return (await apiRequest('/ice-servers')).ice_servers;
  },
};

export {
  actionApi,
  authApi,
  authoringApi,
  buildApi,
  deviceApi,
  environmentApi,
  hubApi,
  knowledgeApi,
  projectApi,
  runApi,
  testApi,
  userApi,
};
