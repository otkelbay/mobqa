import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useProject } from '../../context/ProjectContext.jsx';
import { testApi } from '../../services/apiRepository.js';
import { PRIORITIES, createEmptyStep, parseTags } from '../../services/testStepModel.js';
import { platformLabel } from '../../services/viewModelMappers.js';
import { StepListEditor, stepsValidationError } from '../../components/tests/StepEditor.jsx';
import { T } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import {
  BackLink,
  Button,
  Card,
  ErrorNote,
  Field,
  Page,
  Select,
  TextArea,
  TextInput,
} from '../../components/ui/primitives.jsx';

function ManualTestPage() {
  const navigate = useNavigate();
  const { projectId, project, reload } = useProject();
  const platforms = (project?.platforms || ['android']).map((value) => ({ value, label: platformLabel(value) }));
  const [form, setForm] = React.useState({ title: '', description: '', priority: 'medium', tags: '', platform: '' });
  const [steps, setSteps] = React.useState([createEmptyStep()]);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);
  const set = (key) => (e) => setForm((prev) => ({ ...prev, [key]: e.target.value }));
  const stepsError = stepsValidationError(steps);

  const save = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const created = await testApi.create(projectId, {
        title: form.title.trim(),
        description: form.description.trim(),
        platform: form.platform || platforms[0].value,
        priority: form.priority,
        tags: parseTags(form.tags),
        steps,
      });
      await reload('tests');
      navigate(`/apps/${projectId}/tests/${created.id}`);
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <Page
      title="Write a test"
      subtitle="Say what to do in each step; the agent works out how on the device."
      width={920}
      back={<BackLink onClick={() => navigate(`/apps/${projectId}/tests/new`)}>Back to creation options</BackLink>}
    >
      <form onSubmit={save}>
        <Card>
          <ErrorNote error={error} />
          <Field label="Title">
            <TextInput value={form.title} onChange={set('title')} placeholder="Checkout with a saved card" required />
          </Field>
          <div className={css({ display: 'grid', gridTemplateColumns: '1fr 1fr 2fr', gap: 12 })}>
            <Field label="Platform">
              <Select options={platforms} value={form.platform || platforms[0].value} onChange={set('platform')} />
            </Field>
            <Field label="Priority">
              <Select options={PRIORITIES} value={form.priority} onChange={set('priority')} />
            </Field>
            <Field label="Tags" hint="Comma-separated">
              <TextInput value={form.tags} onChange={set('tags')} placeholder="checkout, smoke" />
            </Field>
          </div>
          <Field label="Description">
            <TextArea value={form.description} onChange={set('description')} rows={2} />
          </Field>
        </Card>
        <Card>
          <StepListEditor steps={steps} onChange={setSteps} />
        </Card>
        {stepsError && <p className={css({ color: T.warning, fontSize: 13, margin: '0 0 14px' })}>{stepsError}</p>}
        <Button type="submit" busy={busy} disabled={Boolean(stepsError) || !form.title.trim()}>
          Create test
        </Button>
      </form>
    </Page>
  );
}

export { ManualTestPage };
