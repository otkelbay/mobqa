import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useProject } from '../../context/ProjectContext.jsx';
import { testApi } from '../../services/apiRepository.js';
import { platformLabel } from '../../services/viewModelMappers.js';
import { T } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import {
  BackLink,
  Button,
  Card,
  ErrorNote,
  Field,
  Page,
  SegmentedTabs,
  TextArea,
} from '../../components/ui/primitives.jsx';
import { IconSparkle } from '../../components/ui/icons.jsx';

function NewTestPage() {
  const navigate = useNavigate();
  const { projectId, project } = useProject();
  const platforms = (project?.platforms || ['android']).map((value) => ({ value, label: platformLabel(value) }));
  const [platform, setPlatform] = React.useState('');
  const [brief, setBrief] = React.useState('');
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);

  const generate = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const generation = await testApi.generate(projectId, {
        platform: platform || platforms[0].value,
        brief: brief.trim(),
      });
      navigate(`/apps/${projectId}/tests/generations/${generation.id}`);
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <Page
      title="Draft tests with the agent"
      subtitle="The model reads the brief, the project description and its knowledge, and drafts tests you can edit."
      width={760}
      back={<BackLink onClick={() => navigate(`/apps/${projectId}/tests/new`)}>Back to creation options</BackLink>}
    >
      <form onSubmit={generate}>
        <Card>
          <ErrorNote error={error} />
          {platforms.length > 1 && (
            <Field label="Platform">
              <SegmentedTabs items={platforms} value={platform || platforms[0].value} onChange={setPlatform} />
            </Field>
          )}
          <Field label="What should the tests cover?">
            <TextArea
              value={brief}
              onChange={(e) => setBrief(e.target.value)}
              rows={7}
              required
              placeholder="Signing in with a phone number and a code, adding a product to the cart, and paying with a saved card. Also check that an empty cart shows a hint."
            />
          </Field>
          <p className={css({ fontSize: 12.5, color: T.textTertiary, margin: '0 0 16px' })}>
            Variable values are never sent to the model; it only sees their names.
          </p>
          <Button type="submit" busy={busy} disabled={!brief.trim()}>
            <IconSparkle size={15} />
            Draft tests
          </Button>
        </Card>
      </form>
    </Page>
  );
}

export { NewTestPage };
