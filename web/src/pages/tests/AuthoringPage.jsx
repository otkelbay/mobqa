import React from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useProject } from '../../context/ProjectContext.jsx';
import { authoringApi } from '../../services/apiRepository.js';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import {
  BackLink,
  Button,
  Card,
  ErrorNote,
  Page,
  SectionLabel,
  SegmentedTabs,
  Spinner,
} from '../../components/ui/primitives.jsx';
import { PlatformBadge, StatusBadge } from '../../components/ui/Badges.jsx';

const ACTIVE = new Set(['queued', 'running']);

// pollInterval matches the agent's pace: one model call and one device action per turn.
const pollInterval = 1500;

const TABS = [
  { value: 'log', label: 'Agent log' },
  { value: 'script', label: 'Script' },
];

function selectorText(selector) {
  if (!selector) return '';
  const parts = [
    selector.id && `id=${selector.id}`,
    selector.text && `text="${selector.text}"`,
    selector.label && `label="${selector.label}"`,
    selector.index > 0 && `#${selector.index}`,
  ];
  return parts.filter(Boolean).join(' ');
}

function LogEntry({ entry }) {
  return (
    <div className={css({ padding: '10px 0', borderBottom: `1px solid ${T.borderDivider}` })}>
      <div className={css({ display: 'flex', gap: 10, alignItems: 'baseline' })}>
        <span className={css({ fontFamily: fonts.mono, fontSize: 12, color: T.textTertiary, width: 52 })}>
          step {entry.step}
        </span>
        <span className={css({ fontFamily: fonts.mono, fontSize: 12.5, fontWeight: 600, color: T.brandText })}>
          {entry.action}
        </span>
        <span className={css({ fontSize: 13, color: T.textPrimary })}>
          {[entry.element, entry.text && `"${entry.text}"`].filter(Boolean).join(' ')}
        </span>
      </div>
      {entry.thought && (
        <div className={css({ fontSize: 12.5, color: T.textSecondary, margin: '4px 0 0 62px', lineHeight: 1.5 })}>
          {entry.thought}
        </div>
      )}
      {entry.error && (
        <div className={css({ fontSize: 12.5, color: T.danger, margin: '4px 0 0 62px' })}>{entry.error}</div>
      )}
    </div>
  );
}

function ScriptOps({ script }) {
  if (script.length === 0) {
    return <div className={css({ fontSize: 13, color: T.textTertiary })}>No operations recorded.</div>;
  }
  return (
    <ol className={css({ margin: 0, padding: 0, listStyle: 'none', fontFamily: fonts.mono, fontSize: 12.5 })}>
      {script.map((op, index) => (
        <li
          key={index}
          className={css({
            display: 'grid',
            gridTemplateColumns: '60px 110px 1fr',
            gap: 10,
            padding: '7px 0',
            borderBottom: `1px solid ${T.borderDivider}`,
          })}
        >
          <span className={css({ color: T.textTertiary })}>step {op.step}</span>
          <span className={css({ fontWeight: 600, color: T.textPrimary })}>{op.kind}</span>
          <span className={css({ color: T.textMedium, wordBreak: 'break-word' })}>
            {[selectorText(op.selector), op.direction, op.text && `"${op.text}"`].filter(Boolean).join(' · ')}
          </span>
        </li>
      ))}
    </ol>
  );
}

function AuthoringPage() {
  const navigate = useNavigate();
  const { authoringId } = useParams();
  const { projectId, can } = useProject();
  const [authoring, setAuthoring] = React.useState(null);
  const [tab, setTab] = React.useState('log');
  const [error, setError] = React.useState(null);
  const active = !authoring || ACTIVE.has(authoring.status);

  React.useEffect(() => {
    if (!active) return undefined;
    let live = true;
    const load = () =>
      authoringApi
        .get(projectId, authoringId)
        .then((next) => live && setAuthoring(next))
        .catch((err) => live && setError(err));
    const timer = setTimeout(load, authoring ? pollInterval : 0);
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [projectId, authoringId, active, authoring]);

  const cancel = async () => {
    try {
      await authoringApi.cancel(projectId, authoringId);
      setAuthoring(await authoringApi.get(projectId, authoringId));
    } catch (err) {
      setError(err);
    }
  };

  const back = authoring && (
    <BackLink onClick={() => navigate(`/apps/${projectId}/tests/${authoring.testId}`)}>Back to the test</BackLink>
  );

  if (!authoring) {
    return <Page title="Authoring">{error ? <ErrorNote error={error} /> : <Spinner />}</Page>;
  }

  return (
    <Page
      title={`Authoring v${authoring.testVersion}`}
      subtitle={`${authoring.deviceName}${authoring.environmentName ? ` · ${authoring.environmentName}` : ''} · ${authoring.created}`}
      back={back}
      actions={
        active &&
        can('engineer') && (
          <Button variant="dangerOutline" onClick={cancel}>
            Stop
          </Button>
        )
      }
    >
      <ErrorNote error={error} />
      <div className={css({ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 18 })}>
        <PlatformBadge platform={authoring.platform} />
        <StatusBadge status={authoring.status} />
        {active && <Spinner size={13} />}
        <span className={css({ fontSize: 12.5, color: T.textTertiary })}>{authoring.duration}</span>
      </div>
      {authoring.failure && (
        <Card style={{ borderColor: T.dangerSubtle }}>
          <SectionLabel>WHY IT STOPPED</SectionLabel>
          <div className={css({ fontSize: 13.5, color: T.danger, lineHeight: 1.55 })}>{authoring.failure}</div>
        </Card>
      )}
      <div className={css({ marginBottom: 14 })}>
        <SegmentedTabs items={TABS} value={tab} onChange={setTab} />
      </div>
      <Card padding={18}>
        {tab === 'log' &&
          (authoring.log.length === 0 ? (
            <div className={css({ fontSize: 13, color: T.textTertiary })}>
              {active ? 'Waiting for the agent’s first move…' : 'The agent did not act.'}
            </div>
          ) : (
            authoring.log.map((entry, index) => <LogEntry key={index} entry={entry} />)
          ))}
        {tab === 'script' && <ScriptOps script={authoring.script} />}
      </Card>
    </Page>
  );
}

export { AuthoringPage };
