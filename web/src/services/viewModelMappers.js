import { stepFromApi } from './testStepModel.js';

function dateFrom(value) {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

function formatDateTime(value, withYear = false) {
  const date = dateFrom(value);
  if (!date) return '—';
  return new Intl.DateTimeFormat('en-US', {
    day: 'numeric',
    month: 'short',
    ...(withYear ? { year: 'numeric' } : {}),
    hour: '2-digit',
    minute: '2-digit',
  }).format(date);
}

function formatDate(value) {
  const date = dateFrom(value);
  if (!date) return '—';
  return new Intl.DateTimeFormat('en-US', { day: 'numeric', month: 'short', year: 'numeric' }).format(date);
}

function formatRelative(value, now = Date.now()) {
  const date = dateFrom(value);
  if (!date) return '—';
  const minutes = Math.max(1, Math.round((now - date.getTime()) / 60000));
  if (minutes < 60) return `${minutes} min ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} h ago`;
  const days = Math.round(hours / 24);
  return days === 1 ? '1 day ago' : `${days} days ago`;
}

function formatDuration(ms) {
  if (!Number.isFinite(ms) || ms <= 0) return '—';
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds} s`;
  return `${Math.floor(seconds / 60)} min ${seconds % 60} s`;
}

function durationBetween(startedAt, finishedAt) {
  const start = dateFrom(startedAt);
  const end = dateFrom(finishedAt);
  return start && end ? Math.max(0, end.getTime() - start.getTime()) : 0;
}

function formatBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes <= 0) return '—';
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

const PLATFORM_LABELS = { android: 'Android', ios: 'iOS' };

function platformLabel(platform) {
  return PLATFORM_LABELS[platform] || platform;
}

function mapProject(project) {
  return {
    id: project.id,
    name: project.name,
    description: project.description || '',
    platforms: project.platforms || [],
    platformLabel: (project.platforms || []).map(platformLabel).join(' · '),
    packageName: project.package_name || '',
    initial: project.appearance?.initial || project.name.slice(0, 1).toUpperCase(),
    color: project.appearance?.color || '#EFF4FF',
    textColor: project.appearance?.text_color || '#1D4ED8',
    updated: formatRelative(project.updated_at),
  };
}

function mapAuthProfile(profile = {}) {
  return {
    method: profile.method || 'none',
    loginVar: profile.login_var || '',
    passwordVar: profile.password_var || '',
    otpOverride: profile.otp?.override || '',
    otpActionId: profile.otp?.action_id || '',
    ssoAccountVar: profile.sso?.account_var || '',
    notes: profile.notes || '',
  };
}

function mapMember(member) {
  return {
    userId: member.user_id,
    email: member.email,
    name: member.name,
    role: member.role,
    joined: formatDate(member.joined_at),
  };
}

function mapProjectDetail(project) {
  return {
    ...mapProject(project),
    role: project.role,
    authProfile: mapAuthProfile(project.auth_profile),
    members: (project.members || []).map(mapMember),
  };
}

function mapBuild(build) {
  const expiresAt = build.provisioning?.expires_at || null;
  return {
    id: build.id,
    platform: build.platform,
    version: build.version,
    buildNumber: build.build_number,
    label: build.build_number ? `${build.version} (${build.build_number})` : build.version,
    fileName: build.file_name,
    size: formatBytes(build.size),
    status: build.status,
    changes: build.analysis?.changes || [],
    isCurrent: build.is_current,
    provisioning: build.provisioning
      ? {
          profileName: build.provisioning.profile_name,
          teamId: build.provisioning.team_id,
          bundleId: build.provisioning.bundle_id,
          expires: formatDate(expiresAt),
          expired: dateFrom(expiresAt) !== null && dateFrom(expiresAt).getTime() <= Date.now(),
        }
      : null,
    uploaded: formatDateTime(build.uploaded_at),
  };
}

function mapDevice(device) {
  return {
    id: device.id,
    hubId: device.hub_id,
    hubName: device.hub_name,
    serial: device.serial,
    name: device.name || device.model || device.serial,
    model: device.model,
    platform: device.platform,
    os: `${platformLabel(device.platform)} ${device.os_version}`.trim(),
    physical: device.physical,
    status: device.status,
    lastSeen: formatRelative(device.last_seen_at),
  };
}

function mapHub(hub) {
  return {
    id: hub.id,
    name: hub.name,
    version: hub.version || '',
    online: hub.online,
    lastSeen: hub.last_seen_at ? formatRelative(hub.last_seen_at) : 'never',
  };
}

const SOURCE_LABELS = { manual: 'manual', generated: 'generated', duplicate: 'copy' };

function mapTestSummary(test) {
  return {
    id: test.id,
    title: test.title,
    description: test.description || '',
    platform: test.platform,
    priority: test.priority,
    tags: test.tags || [],
    source: SOURCE_LABELS[test.source] || test.source,
    version: test.version,
    stepCount: test.step_count,
    updated: formatRelative(test.updated_at),
  };
}

function mapTest(test) {
  return {
    ...mapTestSummary(test),
    steps: (test.steps || []).map((step, index) => stepFromApi(step, `${test.id}-${index}`)),
  };
}

const CHANGE_LABELS = { manual: 'Edited', agent: 'Agent edit', generate: 'Generated', restore: 'Restored' };

function mapVersion(version) {
  return {
    version: version.version,
    title: version.title,
    description: version.description || '',
    stepCount: version.step_count,
    source: version.source,
    sourceLabel: CHANGE_LABELS[version.source] || version.source,
    note: version.note || '',
    created: formatDateTime(version.created_at, true),
    steps: (version.steps || []).map((step, index) => stepFromApi(step, `v${version.version}-${index}`)),
  };
}

function mapAuthoring(authoring) {
  return {
    id: authoring.id,
    testId: authoring.test_id,
    testVersion: authoring.test_version,
    platform: authoring.platform,
    deviceName: authoring.device_name,
    environmentName: authoring.environment_name || '',
    status: authoring.status,
    failure: authoring.failure || '',
    created: formatDateTime(authoring.created_at),
    duration: formatDuration(durationBetween(authoring.started_at, authoring.finished_at)),
    script: authoring.script || [],
    log: authoring.log || [],
  };
}

const ACTIVE_AUTHORING = new Set(['queued', 'running']);

// scriptState says whether a test can run: runs replay the newest succeeded authoring's script, and a
// script made from an older version still runs but no longer matches the steps.
function scriptState(test, authorings = []) {
  if (authorings.some((authoring) => ACTIVE_AUTHORING.has(authoring.status))) return 'authoring';
  const latest = authorings.find((authoring) => authoring.status === 'succeeded');
  if (!latest) return 'none';
  return latest.testVersion === test.version ? 'current' : 'stale';
}

function mapRun(run) {
  return {
    id: run.id,
    platform: run.platform,
    buildLabel: run.build_label || '',
    environmentName: run.environment_name || '',
    status: run.status,
    retries: run.retries,
    summary: run.summary,
    created: formatDateTime(run.created_at),
    duration: formatDuration(durationBetween(run.started_at, run.finished_at)),
  };
}

function mapResult(result) {
  return {
    id: result.id,
    testId: result.test_id || '',
    testVersion: result.test_version,
    title: result.title,
    deviceName: result.device_name,
    status: result.status,
    attempts: result.attempts,
    failedStep: result.failed_step || 0,
    error: result.error || '',
    duration: formatDuration(durationBetween(result.started_at, result.finished_at)),
  };
}

function mapAttempt(attempt) {
  return {
    number: attempt.number,
    status: attempt.status,
    failedStep: attempt.failed_step || 0,
    error: attempt.error || '',
    steps: (attempt.steps || []).map((step) => ({
      status: step.status,
      error: step.error || '',
      duration: formatDuration(step.duration_ms),
    })),
    video: attempt.video
      ? { duration: formatDuration(attempt.video.duration_ms), problem: attempt.video.problem || '' }
      : null,
    duration: formatDuration(durationBetween(attempt.started_at, attempt.finished_at)),
  };
}

function mapNote(note) {
  return {
    id: note.id,
    kind: note.kind,
    title: note.title,
    content: note.content,
    source: note.source,
    pinned: note.pinned,
    indexed: note.indexed,
    updated: formatRelative(note.updated_at),
  };
}

function mapEnvironment(environment) {
  return {
    id: environment.id,
    name: environment.name,
    isDefault: environment.is_default,
    variables: (environment.variables || []).map((variable) => ({
      name: variable.name,
      type: variable.type,
      label: variable.label || '',
      rule: variable.rule || '',
      value: variable.value,
    })),
  };
}

function mapAction(action) {
  return {
    id: action.id,
    name: action.name,
    description: action.description || '',
    request: {
      method: action.request.method,
      url: action.request.url,
      headers: action.request.headers || [],
      body: action.request.body || '',
      expectedStatus: action.request.expected_status || 0,
      extract: action.request.extract || [],
    },
  };
}

export {
  formatDate,
  formatDateTime,
  formatRelative,
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
  platformLabel,
  scriptState,
};
