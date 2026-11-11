import React from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useProject } from '../../context/ProjectContext.jsx';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import { Button, EmptyState, ListRow, Page, SegmentedTabs } from '../../components/ui/primitives.jsx';
import { PlatformBadge, StatusBadge } from '../../components/ui/Badges.jsx';
import { IconChevronRight, IconPlay, IconRuns } from '../../components/ui/icons.jsx';
import { LaunchPanel } from './LaunchPanel.jsx';

const ACTIVE = new Set(['queued', 'running']);

// pollInterval keeps active runs' counters fresh without hammering the API.
const pollInterval = 3000;

const FILTERS = [
  { value: 'all', label: 'All' },
  { value: 'active', label: 'Active' },
  { value: 'passed', label: 'Passed' },
  { value: 'failed', label: 'Failed' },
  { value: 'cancelled', label: 'Cancelled' },
];

function Summary({ summary }) {
  const parts = [
    ['passed', summary.passed, T.success],
    ['flaky', summary.flaky, T.warning],
    ['failed', summary.failed + summary.errored, T.danger],
    ['left', summary.queued + summary.running, T.textTertiary],
  ];
  return (
    <span className={css({ display: 'flex', gap: 10, fontFamily: fonts.mono, fontSize: 12.5 })}>
      {parts
        .filter(([, count]) => count > 0)
        .map(([label, count, color]) => (
          <span key={label} className={css({ color })}>
            {count} {label}
          </span>
        ))}
    </span>
  );
}

function RunRow({ run, onOpen }) {
  return (
    <ListRow onClick={onOpen}>
      <StatusBadge status={run.status} size="sm" />
      <span className={css({ flex: 1, minWidth: 0 })}>
        <span className={css({ display: 'block', fontSize: 14, fontWeight: 600, color: T.textPrimary })}>
          {run.summary.total} results · {run.buildLabel || 'installed app'}
          {run.environmentName && ` · ${run.environmentName}`}
        </span>
        <span className={css({ display: 'block', fontSize: 12.5, color: T.textTertiary, marginTop: 3 })}>
          {run.created} · {run.duration}
        </span>
      </span>
      <Summary summary={run.summary} />
      <PlatformBadge platform={run.platform} />
      <span className={css({ display: 'flex', color: T.textFaint })}>
        <IconChevronRight size={16} viewBox="0 0 16 16" />
      </span>
    </ListRow>
  );
}

function RunsPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const { projectId, runs, reload, can } = useProject();
  const [filter, setFilter] = React.useState('all');
  const preselected = params.get('test');
  const [launching, setLaunching] = React.useState(Boolean(preselected));
  const editable = can('engineer');

  React.useEffect(() => {
    if (!runs.some((run) => ACTIVE.has(run.status))) return undefined;
    const timer = setTimeout(() => reload('runs'), pollInterval);
    return () => clearTimeout(timer);
  }, [runs, reload]);

  const closeLaunch = () => {
    setLaunching(false);
    setParams({});
  };

  const launched = async (run) => {
    await reload('runs');
    navigate(`/apps/${projectId}/runs/${run.id}`);
  };

  const visible = runs.filter(
    (run) => filter === 'all' || (filter === 'active' ? ACTIVE.has(run.status) : run.status === filter),
  );

  return (
    <Page
      title="Runs"
      subtitle="Recorded scripts replayed on hub devices, with retries and a video of every attempt."
      actions={
        editable && (
          <Button onClick={() => setLaunching(true)} disabled={launching}>
            <IconPlay size={13} />
            New run
          </Button>
        )
      }
    >
      {launching && <LaunchPanel preselected={preselected} onLaunched={launched} onCancel={closeLaunch} />}
      {runs.length === 0 ? (
        <EmptyState
          icon={<IconRuns size={28} />}
          title="No runs yet"
          subtitle="Author a test on a device, then run it on as many devices as you like."
        />
      ) : (
        <>
          <div className={css({ marginBottom: 16 })}>
            <SegmentedTabs items={FILTERS} value={filter} onChange={setFilter} />
          </div>
          <div className={css({ display: 'flex', flexDirection: 'column', gap: 9 })}>
            {visible.map((run) => (
              <RunRow key={run.id} run={run} onOpen={() => navigate(`/apps/${projectId}/runs/${run.id}`)} />
            ))}
          </div>
        </>
      )}
    </Page>
  );
}

export { RunsPage };
