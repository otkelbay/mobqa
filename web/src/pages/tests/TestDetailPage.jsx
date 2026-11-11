import React from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useProject } from '../../context/ProjectContext.jsx';
import { useToast } from '../../hooks/useToasts.js';
import { authoringApi, testApi } from '../../services/apiRepository.js';
import { PRIORITIES, parseTags } from '../../services/testStepModel.js';
import { scriptState } from '../../services/viewModelMappers.js';
import { StepList, StepListEditor, stepsValidationError } from '../../components/tests/StepEditor.jsx';
import { DiffRows, VersionHistory } from '../../components/tests/VersionHistory.jsx';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import {
  BackLink,
  Button,
  Card,
  EmptyState,
  ErrorNote,
  Field,
  ListRow,
  Page,
  SegmentedTabs,
  Select,
  Spinner,
  TextArea,
  TextInput,
} from '../../components/ui/primitives.jsx';
import { PlatformBadge, StatusBadge } from '../../components/ui/Badges.jsx';
import { IconPlay, IconSparkle } from '../../components/ui/icons.jsx';

const TABS = [
  { value: 'steps', label: 'Steps' },
  { value: 'authorings', label: 'Authorings' },
  { value: 'history', label: 'History' },
];

const SCRIPT_HINTS = {
  none: 'Runs need a script. Author the test on a device first.',
  stale: 'The steps changed after the last authoring. Runs replay the older script until you author again.',
  current: 'Runs replay the script the agent recorded for this version.',
  authoring: 'The agent is authoring this test on a device.',
};

function StepsEditor({ projectId, test, onSaved, onCancel }) {
  const [form, setForm] = React.useState({
    title: test.title,
    description: test.description,
    priority: test.priority,
    tags: test.tags.join(', '),
  });
  const [steps, setSteps] = React.useState(test.steps);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);
  const set = (key) => (e) => setForm((prev) => ({ ...prev, [key]: e.target.value }));
  const stepsError = stepsValidationError(steps);

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      onSaved(
        await testApi.update(projectId, test.id, {
          baseVersion: test.version,
          title: form.title.trim(),
          description: form.description.trim(),
          priority: form.priority,
          tags: parseTags(form.tags),
          steps,
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
      <Field label="Title">
        <TextInput value={form.title} onChange={set('title')} />
      </Field>
      <div className={css({ display: 'grid', gridTemplateColumns: '1fr 2fr', gap: 12 })}>
        <Field label="Priority">
          <Select options={PRIORITIES} value={form.priority} onChange={set('priority')} />
        </Field>
        <Field label="Tags" hint="Comma-separated">
          <TextInput value={form.tags} onChange={set('tags')} />
        </Field>
      </div>
      <Field label="Description">
        <TextArea value={form.description} onChange={set('description')} rows={2} />
      </Field>
      <StepListEditor steps={steps} onChange={setSteps} />
      {stepsError && <p className={css({ color: T.warning, fontSize: 13 })}>{stepsError}</p>}
      <div className={css({ display: 'flex', gap: 10, marginTop: 16 })}>
        <Button busy={busy} onClick={save} disabled={Boolean(stepsError) || !form.title.trim()}>
          Save as v{test.version + 1}
        </Button>
        <Button variant="secondary" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </Card>
  );
}

// AgentEdit asks the model to change the steps; nothing is saved until the proposal is applied.
function AgentEdit({ projectId, test, onSaved }) {
  const [instruction, setInstruction] = React.useState('');
  const [history, setHistory] = React.useState([]);
  const [proposal, setProposal] = React.useState(null);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);

  const ask = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const next = await testApi.agentEdit(projectId, test.id, instruction.trim(), history);
      setHistory((prev) => [
        ...prev,
        { role: 'user', content: instruction.trim() },
        { role: 'assistant', content: next.reply },
      ]);
      setProposal(next);
      setInstruction('');
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    setBusy(true);
    setError(null);
    try {
      const saved = await testApi.update(projectId, test.id, {
        baseVersion: proposal.baseVersion,
        steps: proposal.steps,
        note: history.findLast((message) => message.role === 'user')?.content,
      });
      setProposal(null);
      setHistory([]);
      onSaved(saved);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card>
      <div className={css({ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 })}>
        <IconSparkle size={16} color={T.brand} />
        <span className={css({ fontSize: 14, fontWeight: 600, color: T.textPrimary })}>Edit with the agent</span>
      </div>
      <ErrorNote error={error} />
      {history.map((message, index) => (
        <div
          key={index}
          className={css({
            fontSize: 13.5,
            lineHeight: 1.55,
            padding: '8px 12px',
            borderRadius: 10,
            marginBottom: 8,
            maxWidth: '85%',
            marginLeft: message.role === 'user' ? 'auto' : 0,
            background: message.role === 'user' ? T.chatUserBg : T.chatAgentBg,
            color: message.role === 'user' ? T.textPrimary : T.chatAgentText,
          })}
        >
          {message.content}
        </div>
      ))}
      {proposal?.changed && (
        <div className={css({ margin: '12px 0' })}>
          <DiffRows before={test.steps} after={proposal.steps} />
          <div className={css({ marginTop: 10 })}>
            <Button size="sm" busy={busy} onClick={apply}>
              Apply as v{proposal.baseVersion + 1}
            </Button>
          </div>
        </div>
      )}
      <form onSubmit={ask} className={css({ display: 'flex', gap: 10 })}>
        <TextInput
          value={instruction}
          onChange={(e) => setInstruction(e.target.value)}
          placeholder="e.g. Sign in with the staging account before step 3"
          style={{ padding: '10px 12px' }}
        />
        <Button type="submit" variant="secondary" busy={busy && !proposal} disabled={!instruction.trim()}>
          Ask
        </Button>
      </form>
    </Card>
  );
}

function AuthorForm({ projectId, test, devices, environments, onStarted }) {
  const usable = devices.filter((device) => device.platform === test.platform && device.status === 'online');
  const defaultEnvironment = environments.find((environment) => environment.isDefault);
  const [deviceId, setDeviceId] = React.useState('');
  const [environmentId, setEnvironmentId] = React.useState('');
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);

  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      onStarted(
        await authoringApi.start(projectId, test.id, {
          deviceId: deviceId || usable[0].id,
          environmentId: environmentId || defaultEnvironment?.id,
        }),
      );
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  if (usable.length === 0) {
    return (
      <p className={css({ fontSize: 13, color: T.textSecondary, margin: '0 0 16px' })}>
        No online {test.platform === 'ios' ? 'iOS' : 'Android'} device. Attach one to a hub to author this test.
      </p>
    );
  }

  return (
    <Card>
      <ErrorNote error={error} />
      <div className={css({ display: 'grid', gridTemplateColumns: '1fr 1fr auto', gap: 12, alignItems: 'end' })}>
        <Field label="Device">
          <Select
            options={usable.map((device) => ({ value: device.id, label: `${device.name} · ${device.hubName}` }))}
            value={deviceId || usable[0].id}
            onChange={(e) => setDeviceId(e.target.value)}
          />
        </Field>
        <Field label="Environment">
          <Select
            options={[
              { value: '', label: 'None' },
              ...environments.map((environment) => ({ value: environment.id, label: environment.name })),
            ]}
            value={environmentId || defaultEnvironment?.id || ''}
            onChange={(e) => setEnvironmentId(e.target.value)}
          />
        </Field>
        <div className={css({ marginBottom: 18 })}>
          <Button busy={busy} onClick={start}>
            Author v{test.version}
          </Button>
        </div>
      </div>
    </Card>
  );
}

function AuthoringList({ projectId, authorings }) {
  const navigate = useNavigate();
  if (authorings.length === 0) {
    return (
      <EmptyState title="Not authored yet" subtitle="The agent records a script the first time it authors the test." />
    );
  }
  return (
    <div className={css({ display: 'flex', flexDirection: 'column', gap: 9 })}>
      {authorings.map((authoring) => (
        <ListRow key={authoring.id} onClick={() => navigate(`/apps/${projectId}/authorings/${authoring.id}`)}>
          <StatusBadge status={authoring.status} size="sm" />
          <span className={css({ fontFamily: fonts.mono, fontSize: 13, color: T.textMuted })}>
            v{authoring.testVersion}
          </span>
          <span className={css({ flex: 1, fontSize: 13.5, color: T.textPrimary })}>
            {authoring.deviceName}
            {authoring.environmentName && ` · ${authoring.environmentName}`}
          </span>
          <span className={css({ fontSize: 12.5, color: T.textTertiary })}>
            {authoring.created} · {authoring.duration}
          </span>
        </ListRow>
      ))}
    </div>
  );
}

function TestHeaderActions({ projectId, test, state, editable, onEdit }) {
  const navigate = useNavigate();
  const addToast = useToast();
  const { reload } = useProject();
  const [confirming, setConfirming] = React.useState(false);

  const act = async (request, done) => {
    try {
      const result = await request();
      await reload('tests');
      done(result);
    } catch (err) {
      addToast({ title: 'Something went wrong', body: err.message, type: 'error' });
    }
  };

  return (
    <div className={css({ display: 'flex', gap: 8 })}>
      {state !== 'none' && (
        <Button onClick={() => navigate(`/apps/${projectId}/runs?test=${test.id}`)}>
          <IconPlay size={13} />
          Run
        </Button>
      )}
      {editable && (
        <>
          <Button variant="secondary" onClick={onEdit}>
            Edit
          </Button>
          <Button
            variant="secondary"
            onClick={() =>
              act(
                () => testApi.duplicate(projectId, test.id),
                (copy) => navigate(`/apps/${projectId}/tests/${copy.id}`),
              )
            }
          >
            Duplicate
          </Button>
          {confirming ? (
            <Button
              variant="danger"
              onClick={() =>
                act(
                  () => testApi.remove(projectId, test.id),
                  () => navigate(`/apps/${projectId}/tests`),
                )
              }
            >
              Delete for good
            </Button>
          ) : (
            <Button variant="ghost" onClick={() => setConfirming(true)}>
              Delete
            </Button>
          )}
        </>
      )}
    </div>
  );
}

function TestDetailPage() {
  const navigate = useNavigate();
  const { testId } = useParams();
  const { projectId, devices, environments, reload, can } = useProject();
  const [test, setTest] = React.useState(null);
  const [authorings, setAuthorings] = React.useState([]);
  const [tab, setTab] = React.useState('steps');
  const [editing, setEditing] = React.useState(false);
  const [error, setError] = React.useState(null);
  const editable = can('engineer');

  React.useEffect(() => {
    Promise.all([testApi.get(projectId, testId), authoringApi.list(projectId, testId)])
      .then(([loaded, list]) => {
        setTest(loaded);
        setAuthorings(list);
      })
      .catch(setError);
  }, [projectId, testId]);

  const saved = async (next) => {
    setTest(next);
    setEditing(false);
    await reload('tests');
  };

  const back = <BackLink onClick={() => navigate(`/apps/${projectId}/tests`)}>Back to tests</BackLink>;
  if (!test) {
    return (
      <Page title="Test" back={back}>
        {error ? <ErrorNote error={error} /> : <Spinner />}
      </Page>
    );
  }

  const state = scriptState(test, authorings);
  return (
    <Page
      title={test.title}
      subtitle={test.description}
      back={back}
      actions={
        <TestHeaderActions
          projectId={projectId}
          test={test}
          state={state}
          editable={editable}
          onEdit={() => {
            setTab('steps');
            setEditing(true);
          }}
        />
      }
    >
      <div className={css({ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 18, flexWrap: 'wrap' })}>
        <PlatformBadge platform={test.platform} />
        <StatusBadge status={state} />
        <span className={css({ fontSize: 12.5, color: T.textTertiary })}>
          v{test.version} · {test.priority} · {test.source} · updated {test.updated}
          {test.tags.length > 0 && ` · ${test.tags.join(', ')}`}
        </span>
      </div>
      <p className={css({ fontSize: 13, color: T.textSecondary, margin: '0 0 18px' })}>{SCRIPT_HINTS[state]}</p>
      <div className={css({ marginBottom: 18 })}>
        <SegmentedTabs items={TABS} value={tab} onChange={setTab} />
      </div>
      {tab === 'steps' &&
        (editing ? (
          <StepsEditor projectId={projectId} test={test} onSaved={saved} onCancel={() => setEditing(false)} />
        ) : (
          <>
            <Card>
              <StepList steps={test.steps} />
            </Card>
            {editable && <AgentEdit key={test.version} projectId={projectId} test={test} onSaved={saved} />}
          </>
        ))}
      {tab === 'authorings' && (
        <>
          {editable && state !== 'authoring' && (
            <AuthorForm
              projectId={projectId}
              test={test}
              devices={devices}
              environments={environments}
              onStarted={(authoring) => navigate(`/apps/${projectId}/authorings/${authoring.id}`)}
            />
          )}
          <AuthoringList projectId={projectId} authorings={authorings} />
        </>
      )}
      {tab === 'history' && <VersionHistory projectId={projectId} test={test} editable={editable} onRestored={saved} />}
    </Page>
  );
}

export { TestDetailPage };
