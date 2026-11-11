import React from 'react';
import { useProject } from '../../context/ProjectContext.jsx';
import { runApi } from '../../services/apiRepository.js';
import { platformLabel } from '../../services/viewModelMappers.js';
import { T } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import { Button, Card, ErrorNote, Field, SegmentedTabs, Select } from '../../components/ui/primitives.jsx';

const RETRIES = [0, 1, 2].map((value) => ({ value: String(value), label: value === 0 ? 'No retries' : `${value}` }));

function CheckList({ items, selected, onToggle, empty }) {
  if (items.length === 0) {
    return <div className={css({ fontSize: 13, color: T.textTertiary, padding: '6px 0' })}>{empty}</div>;
  }
  return (
    <div
      className={css({
        maxHeight: 220,
        overflowY: 'auto',
        border: `1px solid ${T.borderDefault}`,
        borderRadius: 10,
        padding: '4px 0',
      })}
    >
      {items.map((item) => (
        <label
          key={item.id}
          className={css({
            display: 'flex',
            alignItems: 'center',
            gap: 10,
            padding: '8px 12px',
            fontSize: 13.5,
            color: item.disabled ? T.textTertiary : T.textPrimary,
            cursor: item.disabled ? 'not-allowed' : 'pointer',
          })}
        >
          <input
            type="checkbox"
            checked={selected.includes(item.id)}
            disabled={item.disabled}
            onChange={() => onToggle(item.id)}
          />
          <span className={css({ flex: 1 })}>{item.label}</span>
          <span className={css({ fontSize: 12, color: T.textTertiary })}>{item.hint}</span>
        </label>
      ))}
    </div>
  );
}

// LaunchPanel starts a run of tests that share a platform on devices of that platform.
function LaunchPanel({ preselected, onLaunched, onCancel }) {
  const { projectId, project, tests, devices, builds, environments } = useProject();
  const first = tests.find((test) => test.id === preselected);
  const [platform, setPlatform] = React.useState(first?.platform || project?.platforms[0] || 'android');
  const [testIds, setTestIds] = React.useState(first ? [first.id] : []);
  const [deviceIds, setDeviceIds] = React.useState([]);
  const [buildId, setBuildId] = React.useState('');
  const [environmentId, setEnvironmentId] = React.useState(environments.find((env) => env.isDefault)?.id || '');
  const [retries, setRetries] = React.useState('1');
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);

  const toggle = (setter) => (id) =>
    setter((prev) => (prev.includes(id) ? prev.filter((value) => value !== id) : [...prev, id]));
  const choosePlatform = (next) => {
    setPlatform(next);
    setTestIds([]);
    setDeviceIds([]);
    setBuildId('');
  };

  const platformBuilds = builds.filter((build) => build.platform === platform && build.status === 'ready');
  const current = platformBuilds.find((build) => build.isCurrent);
  const buildOptions = [
    { value: '', label: current ? `Current: ${current.label}` : 'No build: the app is already installed' },
    ...platformBuilds.filter((build) => !build.isCurrent).map((build) => ({ value: build.id, label: build.label })),
  ];

  const launch = async () => {
    setBusy(true);
    setError(null);
    try {
      onLaunched(
        await runApi.create(projectId, {
          testIds,
          deviceIds,
          buildId: buildId || undefined,
          environmentId: environmentId || undefined,
          retries: Number(retries),
        }),
      );
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <Card>
      <ErrorNote error={error} />
      {project?.platforms.length > 1 && (
        <Field group label="Platform">
          <SegmentedTabs
            items={project.platforms.map((value) => ({ value, label: platformLabel(value) }))}
            value={platform}
            onChange={choosePlatform}
          />
        </Field>
      )}
      <div className={css({ display: 'grid', gridTemplateColumns: '1.4fr 1fr', gap: 16 })}>
        <Field group label="Tests">
          <CheckList
            items={tests
              .filter((test) => test.platform === platform)
              .map((test) => ({ id: test.id, label: test.title, hint: `v${test.version}` }))}
            selected={testIds}
            onToggle={toggle(setTestIds)}
            empty="No tests for this platform."
          />
        </Field>
        <Field group label="Devices">
          <CheckList
            items={devices
              .filter((device) => device.platform === platform)
              .map((device) => ({
                id: device.id,
                label: device.name,
                hint: device.status,
                disabled: device.status !== 'online',
              }))}
            selected={deviceIds}
            onToggle={toggle(setDeviceIds)}
            empty="No devices for this platform."
          />
        </Field>
      </div>
      <div className={css({ display: 'grid', gridTemplateColumns: '1.4fr 1fr 0.6fr', gap: 16 })}>
        <Field label="Build">
          <Select options={buildOptions} value={buildId} onChange={(e) => setBuildId(e.target.value)} />
        </Field>
        <Field label="Environment">
          <Select
            options={[{ value: '', label: 'None' }, ...environments.map((env) => ({ value: env.id, label: env.name }))]}
            value={environmentId}
            onChange={(e) => setEnvironmentId(e.target.value)}
          />
        </Field>
        <Field label="Retries">
          <Select options={RETRIES} value={retries} onChange={(e) => setRetries(e.target.value)} />
        </Field>
      </div>
      <p className={css({ fontSize: 12.5, color: T.textTertiary, margin: '0 0 16px' })}>
        Each test replays its latest script on every selected device. A test that passes on a retry is marked flaky.
      </p>
      <div className={css({ display: 'flex', gap: 10 })}>
        <Button busy={busy} onClick={launch} disabled={testIds.length === 0 || deviceIds.length === 0}>
          Run {testIds.length} × {deviceIds.length}
        </Button>
        <Button variant="secondary" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </Card>
  );
}

export { LaunchPanel };
