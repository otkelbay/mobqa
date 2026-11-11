import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useProject } from '../../context/ProjectContext.jsx';
import { T } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import { countLabel } from '../../services/viewModelMappers.js';
import { Button, EmptyState, IconChip, ListRow, Page, Select, TextInput } from '../../components/ui/primitives.jsx';
import { PlatformBadge } from '../../components/ui/Badges.jsx';
import { IconChevronRight, IconPlus, IconTests } from '../../components/ui/icons.jsx';

const PRIORITY_COLORS = {
  critical: T.danger,
  high: T.warning,
  medium: T.textMuted,
  low: T.textTertiary,
};

const PRIORITIES = [
  { value: '', label: 'Any priority' },
  { value: 'critical', label: 'Critical' },
  { value: 'high', label: 'High' },
  { value: 'medium', label: 'Medium' },
  { value: 'low', label: 'Low' },
];

const PLATFORMS = [
  { value: '', label: 'Any platform' },
  { value: 'android', label: 'Android' },
  { value: 'ios', label: 'iOS' },
];

function TestRow({ test, onOpen }) {
  return (
    <ListRow onClick={onOpen}>
      <IconChip size={38} bg={T.bgSubtle} color={T.textTertiary} radius={10}>
        <IconTests size={18} />
      </IconChip>
      <span className={css({ flex: 1, minWidth: 0 })}>
        <span
          className={css({
            display: 'block',
            fontSize: 15,
            fontWeight: 600,
            color: T.textPrimary,
            whiteSpace: 'nowrap',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
          })}
        >
          {test.title}
        </span>
        <span className={css({ display: 'flex', alignItems: 'center', gap: 10, marginTop: 5, fontSize: 12.5 })}>
          <span className={css({ fontWeight: 600, color: PRIORITY_COLORS[test.priority] })}>{test.priority}</span>
          <span className={css({ color: T.textTertiary })}>
            {countLabel(test.stepCount, 'step')} · v{test.version} · {test.source} · updated {test.updated}
          </span>
          {test.tags.map((tag) => (
            <span
              key={tag}
              className={css({ padding: '1px 7px', background: T.bgFaint, borderRadius: 6, color: T.textMuted })}
            >
              {tag}
            </span>
          ))}
        </span>
      </span>
      <PlatformBadge platform={test.platform} />
      <span className={css({ display: 'flex', color: T.textFaint, flexShrink: 0 })}>
        <IconChevronRight size={16} viewBox="0 0 16 16" />
      </span>
    </ListRow>
  );
}

function TestsPage() {
  const navigate = useNavigate();
  const { projectId, tests, can } = useProject();
  const [filter, setFilter] = React.useState({ text: '', platform: '', priority: '' });
  const set = (key) => (e) => setFilter((prev) => ({ ...prev, [key]: e.target.value }));
  const newTest = can('engineer') && (
    <Button onClick={() => navigate(`/apps/${projectId}/tests/new`)}>
      <IconPlus size={15} viewBox="0 0 15 15" />
      New test
    </Button>
  );

  const text = filter.text.trim().toLowerCase();
  const visible = tests.filter(
    (test) =>
      (!filter.platform || test.platform === filter.platform) &&
      (!filter.priority || test.priority === filter.priority) &&
      (!text || test.title.toLowerCase().includes(text) || test.tags.some((tag) => tag.includes(text))),
  );

  return (
    <Page
      title="Tests"
      subtitle="Plain-language steps. The agent turns each test into a script on a real device; runs replay it."
      actions={newTest}
    >
      {tests.length === 0 ? (
        <EmptyState
          icon={<IconTests size={28} />}
          title="No tests yet"
          subtitle="Describe what the app should do, or let the agent draft tests from a short brief."
          action={newTest}
        />
      ) : (
        <>
          <div className={css({ display: 'flex', gap: 10, marginBottom: 16 })}>
            <TextInput
              value={filter.text}
              onChange={set('text')}
              placeholder="Search by title or tag"
              style={{ padding: '10px 12px' }}
            />
            <Select options={PLATFORMS} value={filter.platform} onChange={set('platform')} style={{ width: 170 }} />
            <Select options={PRIORITIES} value={filter.priority} onChange={set('priority')} style={{ width: 170 }} />
          </div>
          <div className={css({ display: 'flex', flexDirection: 'column', gap: 9 })}>
            {visible.map((test) => (
              <TestRow key={test.id} test={test} onOpen={() => navigate(`/apps/${projectId}/tests/${test.id}`)} />
            ))}
          </div>
        </>
      )}
    </Page>
  );
}

export { TestsPage };
