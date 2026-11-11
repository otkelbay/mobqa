import React from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useProject } from '../../context/ProjectContext.jsx';
import { testApi } from '../../services/apiRepository.js';
import { T } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import { BackLink, Button, Card, ErrorNote, ListRow, Page, Spinner } from '../../components/ui/primitives.jsx';
import { StatusBadge } from '../../components/ui/Badges.jsx';

// pollInterval matches how long a model call usually takes to make progress worth showing.
const pollInterval = 2000;

function GeneratingPage() {
  const navigate = useNavigate();
  const { generationId } = useParams();
  const { projectId, tests, reload } = useProject();
  const [generation, setGeneration] = React.useState(null);
  const [error, setError] = React.useState(null);
  const status = generation?.status;

  React.useEffect(() => {
    if (status && status !== 'pending') return undefined;
    let live = true;
    const load = async () => {
      try {
        const next = await testApi.generation(projectId, generationId);
        if (!live) return;
        setGeneration(next);
        if (next.status === 'succeeded') reload('tests');
      } catch (err) {
        if (live) setError(err);
      }
    };
    const timer = setTimeout(load, generation ? pollInterval : 0);
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [projectId, generationId, status, generation, reload]);

  const created = tests.filter((test) => generation?.test_ids?.includes(test.id));
  const back = <BackLink onClick={() => navigate(`/apps/${projectId}/tests`)}>Back to tests</BackLink>;

  return (
    <Page title="Drafting tests" subtitle={generation?.brief} width={760} back={back}>
      <ErrorNote error={error} />
      <Card>
        <div className={css({ display: 'flex', alignItems: 'center', gap: 12 })}>
          {(!status || status === 'pending') && <Spinner />}
          <StatusBadge status={status || 'pending'} />
          <span className={css({ fontSize: 13.5, color: T.textSecondary })}>
            {status === 'succeeded' &&
              `${generation.test_ids.length} tests drafted. Review the steps, then author them.`}
            {status === 'failed' && (generation.failure || 'The model could not draft tests from this brief.')}
            {(!status || status === 'pending') && 'The model is reading the brief and what it knows about the app…'}
          </span>
        </div>
      </Card>
      {created.map((test) => (
        <div key={test.id} className={css({ marginBottom: 9 })}>
          <ListRow onClick={() => navigate(`/apps/${projectId}/tests/${test.id}`)}>
            <span className={css({ flex: 1, fontSize: 14.5, fontWeight: 600, color: T.textPrimary })}>
              {test.title}
            </span>
            <span className={css({ fontSize: 12.5, color: T.textTertiary })}>{test.stepCount} steps</span>
          </ListRow>
        </div>
      ))}
      {status === 'failed' && (
        <Button variant="secondary" onClick={() => navigate(`/apps/${projectId}/tests/new/agent`)}>
          Try another brief
        </Button>
      )}
    </Page>
  );
}

export { GeneratingPage };
