import React from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useProject } from '../../context/ProjectContext.jsx';
import { runApi } from '../../services/apiRepository.js';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import { countLabel } from '../../services/viewModelMappers.js';
import { BackLink, Button, Card, ErrorNote, Page, Spinner } from '../../components/ui/primitives.jsx';
import { PlatformBadge, StatusBadge } from '../../components/ui/Badges.jsx';

const ACTIVE = new Set(['queued', 'running']);

// pollInterval matches how quickly a replayed step finishes on a device.
const pollInterval = 2000;

function AttemptVideo({ projectId, runId, resultId, attempt }) {
  const [url, setUrl] = React.useState(null);
  const [error, setError] = React.useState(null);

  React.useEffect(() => () => url && URL.revokeObjectURL(url), [url]);

  if (!attempt.video) return null;
  if (attempt.video.problem) {
    return <div className={css({ fontSize: 12.5, color: T.textTertiary })}>No video: {attempt.video.problem}</div>;
  }
  if (!url) {
    const load = async () => {
      try {
        setUrl(URL.createObjectURL(await runApi.video(projectId, runId, resultId, attempt.number)));
      } catch (err) {
        setError(err);
      }
    };
    return (
      <>
        <ErrorNote error={error} />
        <Button variant="secondary" size="sm" onClick={load}>
          Watch video · {attempt.video.duration}
        </Button>
      </>
    );
  }
  return (
    <video
      src={url}
      controls
      className={css({ maxHeight: 520, maxWidth: '100%', borderRadius: 10, background: '#000' })}
    />
  );
}

function Attempt({ projectId, runId, resultId, attempt }) {
  return (
    <div className={css({ padding: '14px 0', borderTop: `1px solid ${T.borderDivider}` })}>
      <div className={css({ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10 })}>
        <span className={css({ fontSize: 13, fontWeight: 600, color: T.textPrimary })}>Attempt {attempt.number}</span>
        <StatusBadge status={attempt.status} size="sm" />
        <span className={css({ fontSize: 12.5, color: T.textTertiary })}>{attempt.duration}</span>
      </div>
      {attempt.error && (
        <div className={css({ fontSize: 13, color: T.danger, marginBottom: 10 })}>
          {attempt.failedStep > 0 && `Step ${attempt.failedStep}: `}
          {attempt.error}
        </div>
      )}
      <div className={css({ display: 'flex', flexWrap: 'wrap', gap: 6, marginBottom: 12 })}>
        {attempt.steps.map((step, index) => (
          <span
            key={index}
            title={step.error || `Step ${index + 1} · ${step.duration}`}
            className={css({
              fontFamily: fonts.mono,
              fontSize: 11.5,
              padding: '3px 8px',
              borderRadius: 6,
              background:
                step.status === 'passed' ? T.successSubtle : step.status === 'failed' ? T.dangerSubtle : T.bgFaint,
              color: step.status === 'passed' ? T.success : step.status === 'failed' ? T.danger : T.textTertiary,
            })}
          >
            {index + 1}
          </span>
        ))}
      </div>
      <AttemptVideo projectId={projectId} runId={runId} resultId={resultId} attempt={attempt} />
    </div>
  );
}

function ResultRow({ projectId, runId, result }) {
  const [open, setOpen] = React.useState(false);
  const [attempts, setAttempts] = React.useState(null);

  const toggle = async () => {
    setOpen((value) => !value);
    if (!attempts && result.attempts > 0) setAttempts(await runApi.attempts(projectId, runId, result.id));
  };

  return (
    <Card padding={0} style={{ marginBottom: 9 }}>
      <button
        type="button"
        onClick={toggle}
        className={css({
          display: 'flex',
          alignItems: 'center',
          gap: 12,
          width: '100%',
          padding: '14px 18px',
          border: 'none',
          background: 'transparent',
          cursor: 'pointer',
          fontFamily: fonts.body,
          textAlign: 'left',
        })}
      >
        <span className={css({ display: 'flex', width: 96, flexShrink: 0 })}>
          <StatusBadge status={result.status} size="sm" />
        </span>
        <span className={css({ flex: 1, minWidth: 0 })}>
          <span className={css({ display: 'block', fontSize: 14, fontWeight: 600, color: T.textPrimary })}>
            {result.title}
          </span>
          <span className={css({ display: 'block', fontSize: 12.5, color: T.textTertiary, marginTop: 3 })}>
            {result.deviceName} · v{result.testVersion} · {countLabel(result.attempts, 'attempt')} · {result.duration}
          </span>
        </span>
        {result.error && (
          <span className={css({ fontSize: 12.5, color: T.danger, maxWidth: 320, textAlign: 'right' })}>
            {result.failedStep > 0 && `Step ${result.failedStep}: `}
            {result.error}
          </span>
        )}
      </button>
      {open && (
        <div className={css({ padding: '0 18px 6px' })}>
          {result.attempts === 0 && (
            <div className={css({ fontSize: 13, color: T.textTertiary, padding: '0 0 12px' })}>No attempts yet.</div>
          )}
          {result.attempts > 0 && !attempts && <Spinner />}
          {attempts?.map((attempt) => (
            <Attempt key={attempt.number} projectId={projectId} runId={runId} resultId={result.id} attempt={attempt} />
          ))}
        </div>
      )}
    </Card>
  );
}

function RunDetailPage() {
  const navigate = useNavigate();
  const { runId } = useParams();
  const { projectId, reload, can } = useProject();
  const [data, setData] = React.useState(null);
  const [error, setError] = React.useState(null);
  const active = !data || ACTIVE.has(data.run.status);

  React.useEffect(() => {
    if (!active) return undefined;
    let live = true;
    const load = () =>
      runApi
        .get(projectId, runId)
        .then((next) => {
          if (!live) return;
          setData(next);
          if (!ACTIVE.has(next.run.status)) reload('runs');
        })
        .catch((err) => live && setError(err));
    const timer = setTimeout(load, data ? pollInterval : 0);
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [projectId, runId, active, data, reload]);

  const cancel = async () => {
    try {
      await runApi.cancel(projectId, runId);
      setData(await runApi.get(projectId, runId));
    } catch (err) {
      setError(err);
    }
  };

  const back = <BackLink onClick={() => navigate(`/apps/${projectId}/runs`)}>Back to runs</BackLink>;
  if (!data) {
    return (
      <Page title="Run" back={back}>
        {error ? <ErrorNote error={error} /> : <Spinner />}
      </Page>
    );
  }

  const { run, results } = data;
  return (
    <Page
      title={`Run · ${run.created}`}
      subtitle={`${run.buildLabel || 'Installed app'}${run.environmentName ? ` · ${run.environmentName}` : ''} · up to ${countLabel(run.retries, 'retry')}`}
      back={back}
      actions={
        active &&
        can('engineer') && (
          <Button variant="dangerOutline" onClick={cancel}>
            Stop run
          </Button>
        )
      }
    >
      <ErrorNote error={error} />
      <div className={css({ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 20 })}>
        <PlatformBadge platform={run.platform} />
        <StatusBadge status={run.status} />
        {active && <Spinner size={13} />}
        <span className={css({ fontFamily: fonts.mono, fontSize: 12.5, color: T.textSecondary })}>
          {run.summary.passed}/{run.summary.total} passed
          {run.summary.flaky > 0 && ` · ${run.summary.flaky} flaky`}
          {run.summary.failed + run.summary.errored > 0 && ` · ${run.summary.failed + run.summary.errored} failed`}
          {' · '}
          {run.duration}
        </span>
      </div>
      {results.map((result) => (
        <ResultRow key={result.id} projectId={projectId} runId={runId} result={result} />
      ))}
    </Page>
  );
}

export { RunDetailPage };
