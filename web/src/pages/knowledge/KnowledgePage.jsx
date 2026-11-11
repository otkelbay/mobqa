import React from 'react';
import { useProject } from '../../context/ProjectContext.jsx';
import { useToast } from '../../hooks/useToasts.js';
import { knowledgeApi } from '../../services/apiRepository.js';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import {
  Button,
  Card,
  EmptyState,
  ErrorNote,
  Field,
  Page,
  SegmentedTabs,
  Select,
  Spinner,
  TextArea,
  TextInput,
} from '../../components/ui/primitives.jsx';
import { IconMemory, IconPlus } from '../../components/ui/icons.jsx';

const KINDS = [
  { value: 'note', label: 'Note', hint: 'Facts about the app' },
  { value: 'flow', label: 'Flow', hint: 'How a journey goes' },
  { value: 'gotcha', label: 'Gotcha', hint: 'What trips the agent up' },
  { value: 'selector', label: 'Selector', hint: 'Where an element is' },
];

const KIND_LABELS = Object.fromEntries(KINDS.map((kind) => [kind.value, kind.label]));
const FILTERS = [{ value: 'all', label: 'All' }, ...KINDS.map(({ value, label }) => ({ value, label }))];

function Composer({ projectId, note, onDone }) {
  const addToast = useToast();
  const [draft, setDraft] = React.useState(note);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);
  const set = (key) => (e) => setDraft((prev) => ({ ...prev, [key]: e.target.value }));

  const save = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    const fields = { kind: draft.kind, title: draft.title.trim(), content: draft.content.trim(), pinned: draft.pinned };
    try {
      if (draft.id) await knowledgeApi.update(projectId, draft.id, fields);
      else await knowledgeApi.create(projectId, fields);
      addToast({ title: 'Note saved', body: 'The agent can use it once it is indexed.', type: 'success' });
      await onDone();
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <Card>
      <form onSubmit={save}>
        <ErrorNote error={error} />
        <div className={css({ display: 'grid', gridTemplateColumns: '180px 1fr', gap: 12 })}>
          <Field label="Kind">
            <Select options={KINDS} value={draft.kind} onChange={set('kind')} />
          </Field>
          <Field label="Title">
            <TextInput
              value={draft.title}
              onChange={set('title')}
              placeholder="Checkout asks for a delivery slot"
              required
            />
          </Field>
        </div>
        <Field label="Content">
          <TextArea value={draft.content} onChange={set('content')} rows={6} required />
        </Field>
        <label
          className={css({
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            fontSize: 13,
            color: T.textMedium,
            marginBottom: 18,
          })}
        >
          <input
            type="checkbox"
            checked={draft.pinned}
            onChange={(e) => setDraft((prev) => ({ ...prev, pinned: e.target.checked }))}
          />
          Pinned — always given to the agent, not only when relevant
        </label>
        <div className={css({ display: 'flex', gap: 10 })}>
          <Button type="submit" busy={busy}>
            Save
          </Button>
          <Button variant="secondary" onClick={onDone}>
            Cancel
          </Button>
        </div>
      </form>
    </Card>
  );
}

function Chip({ children, tone = 'neutral' }) {
  const tones = {
    neutral: { background: T.bgFaint, color: T.textMuted },
    brand: { background: T.brandSubtle, color: T.brandText },
    warning: { background: T.warningSubtle, color: T.warning },
  };
  return (
    <span
      className={css({
        fontSize: 11.5,
        fontWeight: 600,
        padding: '3px 8px',
        borderRadius: 6,
        flexShrink: 0,
        ...tones[tone],
      })}
    >
      {children}
    </span>
  );
}

function NoteRow({ note, editable, onEdit, onDelete }) {
  return (
    <Card padding={18} style={{ marginBottom: 10 }}>
      <div className={css({ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 })}>
        <Chip tone="brand">{KIND_LABELS[note.kind] || note.kind}</Chip>
        {note.source === 'agent' && <Chip>Learned by the agent</Chip>}
        {note.pinned && <Chip>Pinned</Chip>}
        {!note.indexed && <Chip tone="warning">Indexing…</Chip>}
        <span className={css({ flex: 1 })} />
        <span className={css({ fontSize: 12, color: T.textTertiary })}>{note.updated}</span>
      </div>
      <div className={css({ fontSize: 14.5, fontWeight: 600, color: T.textPrimary, marginBottom: 6 })}>
        {note.title}
      </div>
      <div className={css({ fontSize: 13.5, color: T.textMedium, lineHeight: 1.6, whiteSpace: 'pre-wrap' })}>
        {note.content}
      </div>
      {editable && (
        <div className={css({ display: 'flex', gap: 8, marginTop: 12 })}>
          <Button variant="secondary" size="sm" onClick={onEdit}>
            Edit
          </Button>
          <Button variant="ghost" size="sm" onClick={onDelete}>
            Delete
          </Button>
        </div>
      )}
    </Card>
  );
}

function SearchResults({ matches }) {
  if (matches.length === 0) {
    return (
      <div className={css({ fontSize: 13, color: T.textTertiary, padding: '6px 2px 18px' })}>Nothing relevant.</div>
    );
  }
  return (
    <div className={css({ marginBottom: 22 })}>
      {matches.map((match) => (
        <div
          key={`${match.note_id}-${match.excerpt}`}
          className={css({ borderLeft: `3px solid ${T.brandBorder}`, padding: '6px 14px', marginBottom: 10 })}
        >
          <div className={css({ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 4 })}>
            <span className={css({ fontSize: 13.5, fontWeight: 600, color: T.textPrimary })}>{match.title}</span>
            <span className={css({ fontSize: 11.5, color: T.textTertiary, fontFamily: fonts.mono })}>
              {Math.round(match.similarity * 100)}%
            </span>
          </div>
          <div className={css({ fontSize: 13, color: T.textMedium, lineHeight: 1.55 })}>{match.excerpt}</div>
        </div>
      ))}
    </div>
  );
}

function KnowledgePage() {
  const { projectId, can } = useProject();
  const addToast = useToast();
  const [notes, setNotes] = React.useState(null);
  const [filter, setFilter] = React.useState('all');
  const [editing, setEditing] = React.useState(null);
  const [search, setSearch] = React.useState('');
  const [matches, setMatches] = React.useState(null);
  const [searching, setSearching] = React.useState(false);
  const [error, setError] = React.useState(null);
  const editable = can('engineer');

  const load = React.useCallback(async () => {
    try {
      setNotes(await knowledgeApi.list(projectId));
    } catch (err) {
      setError(err);
    }
  }, [projectId]);

  React.useEffect(() => {
    load();
  }, [load]);

  // Notes are embedded in the background; poll until every note is searchable.
  React.useEffect(() => {
    if (!notes?.some((note) => !note.indexed)) return undefined;
    const timer = setTimeout(load, 3000);
    return () => clearTimeout(timer);
  }, [notes, load]);

  const done = async () => {
    setEditing(null);
    await load();
  };

  const remove = async (note) => {
    try {
      await knowledgeApi.remove(projectId, note.id);
      await load();
    } catch (err) {
      addToast({ title: 'Could not delete the note', body: err.message, type: 'error' });
    }
  };

  const runSearch = async (e) => {
    e.preventDefault();
    if (!search.trim()) {
      setMatches(null);
      return;
    }
    setSearching(true);
    try {
      setMatches(await knowledgeApi.search(projectId, search.trim()));
    } catch (err) {
      setError(err);
    } finally {
      setSearching(false);
    }
  };

  const visible = (notes || [])
    .filter((note) => filter === 'all' || note.kind === filter)
    .sort((a, b) => Number(b.pinned) - Number(a.pinned));

  return (
    <Page
      title="Knowledge"
      subtitle="What the agent should know about the app. Relevant notes are retrieved into every authoring; after each one the agent writes down what it learned."
      actions={
        editable && (
          <Button
            onClick={() => setEditing({ kind: 'note', title: '', content: '', pinned: false })}
            disabled={Boolean(editing)}
          >
            <IconPlus size={15} viewBox="0 0 15 15" />
            New note
          </Button>
        )
      }
    >
      <ErrorNote error={error} />
      {editing && <Composer key={editing.id || 'new'} projectId={projectId} note={editing} onDone={done} />}
      <form onSubmit={runSearch} className={css({ display: 'flex', gap: 10, marginBottom: 14 })}>
        <TextInput
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Ask what the agent would retrieve, e.g. how to pay with a saved card"
          style={{ padding: '11px 13px' }}
        />
        <Button type="submit" variant="secondary" busy={searching}>
          Search
        </Button>
      </form>
      {matches && <SearchResults matches={matches} />}
      <div className={css({ marginBottom: 16 })}>
        <SegmentedTabs items={FILTERS} value={filter} onChange={setFilter} />
      </div>
      {notes === null ? (
        <Spinner />
      ) : visible.length === 0 ? (
        <EmptyState
          icon={<IconMemory size={28} />}
          title="No notes yet"
          subtitle="Add how sign-in works, which screens are tricky, or test data the app expects."
        />
      ) : (
        visible.map((note) => (
          <NoteRow
            key={note.id}
            note={note}
            editable={editable}
            onEdit={() => setEditing(note)}
            onDelete={() => remove(note)}
          />
        ))
      )}
    </Page>
  );
}

export { KnowledgePage };
