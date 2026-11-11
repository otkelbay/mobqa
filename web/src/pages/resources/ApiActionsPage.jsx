import React from 'react';
import { useProject } from '../../context/ProjectContext.jsx';
import { useToast } from '../../hooks/useToasts.js';
import { actionApi } from '../../services/apiRepository.js';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import {
  Button,
  Card,
  EmptyState,
  ErrorNote,
  Field,
  ListRow,
  Page,
  Select,
  TextArea,
  TextInput,
} from '../../components/ui/primitives.jsx';
import { IconApis, IconPlus } from '../../components/ui/icons.jsx';

const METHOD_COLOR = {
  GET: '#1A8350',
  POST: '#5E43F0',
  PUT: '#C98A1E',
  PATCH: '#C98A1E',
  DELETE: '#D9483B',
};

const METHODS = Object.keys(METHOD_COLOR).map((method) => ({ value: method, label: method }));

const compact = { padding: '9px 11px', fontSize: 13 };

function emptyAction() {
  return {
    name: '',
    description: '',
    request: { method: 'GET', url: '', headers: [], body: '', expectedStatus: 200, extract: [] },
  };
}

// PairList edits a list of two-field rows, such as headers or extractions.
function PairList({ rows, keys, placeholders, onChange, mono }) {
  const set = (index, key, value) => onChange(rows.map((row, i) => (i === index ? { ...row, [key]: value } : row)));
  return (
    <>
      {rows.map((row, index) => (
        <div key={index} className={css({ display: 'flex', gap: 8, marginBottom: 8 })}>
          {keys.map((key, k) => (
            <TextInput
              key={key}
              value={row[key]}
              onChange={(e) => set(index, key, e.target.value)}
              placeholder={placeholders[k]}
              style={{ ...compact, ...(mono ? { fontFamily: fonts.mono } : {}) }}
            />
          ))}
          <Button variant="ghost" size="sm" onClick={() => onChange(rows.filter((_, i) => i !== index))}>
            ×
          </Button>
        </div>
      ))}
      <Button
        variant="secondary"
        size="sm"
        onClick={() => onChange([...rows, Object.fromEntries(keys.map((key) => [key, '']))])}
      >
        Add
      </Button>
    </>
  );
}

function ActionEditor({ projectId, action, onDone }) {
  const addToast = useToast();
  const [draft, setDraft] = React.useState(action);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);
  const setRequest = (key, value) => setDraft((prev) => ({ ...prev, request: { ...prev.request, [key]: value } }));

  const run = async (request, title) => {
    setBusy(true);
    setError(null);
    try {
      await request();
      addToast({ title, type: 'success' });
      await onDone();
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  const save = () =>
    run(async () => {
      const action = { ...draft, name: draft.name.trim() };
      if (draft.id) await actionApi.replace(projectId, draft.id, action);
      else await actionApi.create(projectId, action);
    }, 'API action saved');
  const remove = () => run(() => actionApi.remove(projectId, draft.id), 'API action deleted');

  return (
    <Card>
      <ErrorNote error={error} />
      <Field label="Name" hint="Steps call the action by this name.">
        <TextInput
          value={draft.name}
          onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          placeholder="Fetch OTP"
        />
      </Field>
      <Field label="Description">
        <TextInput value={draft.description} onChange={(e) => setDraft({ ...draft, description: e.target.value })} />
      </Field>
      <Field label="Request" hint="Use {{NAME}} for environment variables, e.g. {{API_URL}}/otp?phone={{PHONE}}.">
        <div className={css({ display: 'flex', gap: 8 })}>
          <Select
            options={METHODS}
            value={draft.request.method}
            onChange={(e) => setRequest('method', e.target.value)}
            style={{ width: 120, ...compact }}
          />
          <TextInput
            value={draft.request.url}
            onChange={(e) => setRequest('url', e.target.value)}
            placeholder="https://staging.example.com/otp"
            style={{ ...compact, fontFamily: fonts.mono }}
          />
        </div>
      </Field>
      <Field label="Headers">
        <PairList
          rows={draft.request.headers}
          keys={['name', 'value']}
          placeholders={['Authorization', 'Bearer {{TOKEN}}']}
          onChange={(headers) => setRequest('headers', headers)}
          mono
        />
      </Field>
      {draft.request.method !== 'GET' && (
        <Field label="Body">
          <TextArea
            value={draft.request.body}
            onChange={(e) => setRequest('body', e.target.value)}
            rows={4}
            style={{ fontFamily: fonts.mono, fontSize: 13 }}
          />
        </Field>
      )}
      <Field label="Expected status">
        <TextInput
          type="number"
          value={draft.request.expectedStatus}
          onChange={(e) => setRequest('expectedStatus', e.target.value)}
          style={{ ...compact, width: 120 }}
        />
      </Field>
      <Field label="Extract into variables" hint="A regular expression with one group, applied to the response body.">
        <PairList
          rows={draft.request.extract}
          keys={['variable', 'pattern']}
          placeholders={['OTP', '"code":"(\\d+)"']}
          onChange={(extract) => setRequest('extract', extract)}
          mono
        />
      </Field>
      <div className={css({ display: 'flex', gap: 10, marginTop: 6 })}>
        <Button busy={busy} onClick={save} disabled={!draft.name.trim() || !draft.request.url.trim()}>
          Save
        </Button>
        <Button variant="secondary" onClick={onDone}>
          Cancel
        </Button>
        <span className={css({ flex: 1 })} />
        {draft.id && (
          <Button variant="dangerOutline" onClick={remove} disabled={busy}>
            Delete
          </Button>
        )}
      </div>
    </Card>
  );
}

function ApiActionsPage() {
  const { projectId, actions, reload, can } = useProject();
  const [editing, setEditing] = React.useState(null);
  const editable = can('engineer');

  const done = async () => {
    setEditing(null);
    await reload('actions');
  };

  return (
    <Page
      title="API actions"
      subtitle="HTTP calls a test runs with a call step, such as reading the code a backend sent. Hubs send them, not the server."
      actions={
        editable && (
          <Button onClick={() => setEditing(emptyAction())} disabled={Boolean(editing)}>
            <IconPlus size={15} viewBox="0 0 15 15" />
            New action
          </Button>
        )
      }
    >
      {editing && <ActionEditor key={editing.id || 'new'} projectId={projectId} action={editing} onDone={done} />}
      {actions.length === 0 && !editing ? (
        <EmptyState
          icon={<IconApis size={28} />}
          title="No API actions"
          subtitle="Create a reusable API call — any test in the project can run it as a step."
        />
      ) : (
        <div className={css({ display: 'flex', flexDirection: 'column', gap: 9 })}>
          {actions.map((action) => (
            <ListRow key={action.id} onClick={editable ? () => setEditing(action) : undefined}>
              <span
                className={css({
                  width: 52,
                  fontSize: 11,
                  fontWeight: 700,
                  color: METHOD_COLOR[action.request.method] || T.textMuted,
                  fontFamily: fonts.mono,
                  flexShrink: 0,
                })}
              >
                {action.request.method}
              </span>
              <span className={css({ flex: 1, minWidth: 0 })}>
                <span className={css({ display: 'block', fontSize: 14, fontWeight: 600, color: T.textPrimary })}>
                  {action.name}
                </span>
                <span
                  className={css({
                    display: 'block',
                    fontSize: 12.5,
                    color: T.textTertiary,
                    marginTop: 3,
                    fontFamily: fonts.mono,
                    whiteSpace: 'nowrap',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                  })}
                >
                  {action.request.url}
                </span>
              </span>
              {action.request.extract.length > 0 && (
                <span className={css({ fontSize: 12, color: T.textMuted, fontFamily: fonts.mono, flexShrink: 0 })}>
                  → {action.request.extract.map((e) => `{{${e.variable}}}`).join(' ')}
                </span>
              )}
            </ListRow>
          ))}
        </div>
      )}
    </Page>
  );
}

export { ApiActionsPage };
