import React from 'react';
import { useProject } from '../../context/ProjectContext.jsx';
import { useToast } from '../../hooks/useToasts.js';
import { environmentApi } from '../../services/apiRepository.js';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import { Button, Card, EmptyState, ErrorNote, Page, Select, TextInput } from '../../components/ui/primitives.jsx';
import { IconPlus, IconSecrets } from '../../components/ui/icons.jsx';

const TYPES = [
  { value: 'string', label: 'Text' },
  { value: 'secret', label: 'Secret' },
  { value: 'phone', label: 'Phone' },
  { value: 'email', label: 'Email' },
  { value: 'number', label: 'Number' },
  { value: 'url', label: 'URL' },
];

const compact = { padding: '9px 11px', fontSize: 13 };

function emptyVariable() {
  return { name: '', type: 'string', label: '', rule: '', value: '' };
}

function VariableRow({ variable, editable, onChange, onRemove }) {
  const set = (key) => (e) => onChange({ ...variable, [key]: e.target.value });
  return (
    <div
      className={css({ display: 'grid', gridTemplateColumns: '1.1fr 0.8fr 1.4fr 1.2fr auto', gap: 8, marginBottom: 8 })}
    >
      <TextInput
        value={variable.name}
        onChange={set('name')}
        placeholder="LOGIN"
        disabled={!editable}
        style={{ ...compact, fontFamily: fonts.mono }}
      />
      <Select options={TYPES} value={variable.type} onChange={set('type')} disabled={!editable} style={compact} />
      <TextInput
        type={variable.type === 'secret' ? 'password' : 'text'}
        value={variable.value}
        onChange={set('value')}
        placeholder={variable.type === 'secret' ? 'Kept unless changed' : 'Value'}
        disabled={!editable}
        style={compact}
      />
      <TextInput
        value={variable.rule}
        onChange={set('rule')}
        placeholder="Hint, e.g. +7 and ten digits"
        disabled={!editable}
        style={compact}
      />
      {editable ? (
        <Button variant="ghost" size="sm" onClick={onRemove}>
          ×
        </Button>
      ) : (
        <span />
      )}
    </div>
  );
}

function EnvironmentCard({ projectId, environment, editable, onSaved, onRemoved }) {
  const addToast = useToast();
  const [draft, setDraft] = React.useState(environment);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);

  React.useEffect(() => setDraft(environment), [environment]);

  const setVariable = (index, next) =>
    setDraft((prev) => ({ ...prev, variables: prev.variables.map((v, i) => (i === index ? next : v)) }));
  const removeVariable = (index) =>
    setDraft((prev) => ({ ...prev, variables: prev.variables.filter((_, i) => i !== index) }));

  const run = async (request, title) => {
    setBusy(true);
    setError(null);
    try {
      await request();
      addToast({ title, type: 'success' });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const save = () =>
    run(async () => {
      const variables = draft.variables.map((v) => ({ ...v, name: v.name.trim() }));
      if (environment.id) {
        await environmentApi.update(projectId, environment.id, { ...draft, variables });
      } else {
        await environmentApi.create(projectId, { ...draft, variables });
      }
      await onSaved();
    }, 'Environment saved');

  const remove = () =>
    run(async () => {
      if (environment.id) await environmentApi.remove(projectId, environment.id);
      await onRemoved();
    }, 'Environment deleted');

  return (
    <Card>
      <ErrorNote error={error} />
      <div className={css({ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 })}>
        <TextInput
          value={draft.name}
          onChange={(e) => setDraft((prev) => ({ ...prev, name: e.target.value }))}
          placeholder="Staging"
          disabled={!editable}
          style={{ ...compact, fontSize: 15, fontWeight: 600, maxWidth: 260 }}
        />
        <label className={css({ display: 'flex', alignItems: 'center', gap: 7, fontSize: 13, color: T.textMedium })}>
          <input
            type="checkbox"
            checked={draft.isDefault}
            disabled={!editable}
            onChange={(e) => setDraft((prev) => ({ ...prev, isDefault: e.target.checked }))}
          />
          Default for runs and authorings
        </label>
      </div>
      {draft.variables.map((variable, index) => (
        <VariableRow
          key={index}
          variable={variable}
          editable={editable}
          onChange={(next) => setVariable(index, next)}
          onRemove={() => removeVariable(index)}
        />
      ))}
      {editable && (
        <div className={css({ display: 'flex', gap: 10, marginTop: 14 })}>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setDraft((prev) => ({ ...prev, variables: [...prev.variables, emptyVariable()] }))}
          >
            Add variable
          </Button>
          <span className={css({ flex: 1 })} />
          <Button variant="ghost" size="sm" onClick={remove} disabled={busy}>
            Delete
          </Button>
          <Button size="sm" busy={busy} onClick={save} disabled={!draft.name.trim()}>
            Save
          </Button>
        </div>
      )}
    </Card>
  );
}

function EnvironmentsPage() {
  const { projectId, environments, reload, can } = useProject();
  const [adding, setAdding] = React.useState(false);
  const editable = can('engineer');

  const reloadAll = async () => {
    setAdding(false);
    await reload('environments');
  };

  return (
    <Page
      title="Environments"
      subtitle="Variables that steps and the sign-in profile refer to as {{NAME}}. Secrets are encrypted and shown masked."
      actions={
        editable && (
          <Button onClick={() => setAdding(true)} disabled={adding}>
            <IconPlus size={15} viewBox="0 0 15 15" />
            New environment
          </Button>
        )
      }
    >
      {adding && (
        <EnvironmentCard
          projectId={projectId}
          environment={{ name: '', isDefault: environments.length === 0, variables: [emptyVariable()] }}
          editable
          onSaved={reloadAll}
          onRemoved={async () => setAdding(false)}
        />
      )}
      {environments.length === 0 && !adding ? (
        <EmptyState
          icon={<IconSecrets size={28} />}
          title="No environments"
          subtitle="Add the test accounts and endpoints your tests need, such as a staging login and password."
        />
      ) : (
        environments.map((environment) => (
          <EnvironmentCard
            key={environment.id}
            projectId={projectId}
            environment={environment}
            editable={editable}
            onSaved={reloadAll}
            onRemoved={reloadAll}
          />
        ))
      )}
    </Page>
  );
}

export { EnvironmentsPage };
