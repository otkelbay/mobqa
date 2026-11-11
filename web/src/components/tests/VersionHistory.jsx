import React from 'react';
import { diffSteps, diffSummary } from '../../services/stepDiff.js';
import { actionLabel } from '../../services/testStepModel.js';
import { testApi } from '../../services/apiRepository.js';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import { Button, Card, ErrorNote, Spinner } from '../ui/primitives.jsx';

const ROW_TONES = {
  added: { mark: '+', color: T.success, background: T.successSubtle },
  removed: { mark: '−', color: T.danger, background: T.dangerSubtle },
  changed: { mark: '~', color: T.warning, background: T.warningSubtle },
  same: { mark: ' ', color: T.textTertiary, background: 'transparent' },
};

function stepLine(step) {
  return [actionLabel(step.action), step.target, step.expectedResult && `→ ${step.expectedResult}`]
    .filter(Boolean)
    .join(' · ');
}

// DiffRows shows how one list of steps became another, unchanged steps included for context.
function DiffRows({ before, after }) {
  const rows = diffSteps(before, after);
  return (
    <div className={css({ fontFamily: fonts.mono, fontSize: 12.5 })}>
      {rows.map((row, index) => {
        const tone = ROW_TONES[row.type];
        return (
          <div
            key={index}
            className={css({
              display: 'grid',
              gridTemplateColumns: '18px 1fr',
              padding: '5px 8px',
              borderRadius: 6,
              background: tone.background,
              color: row.type === 'same' ? T.textSecondary : T.textPrimary,
            })}
          >
            <span className={css({ color: tone.color, fontWeight: 700 })}>{tone.mark}</span>
            <span>
              {row.type === 'changed' && (
                <span className={css({ display: 'block', color: T.textTertiary, textDecoration: 'line-through' })}>
                  {stepLine(row.oldStep)}
                </span>
              )}
              {stepLine(row.step)}
            </span>
          </div>
        );
      })}
    </div>
  );
}

function summaryText(rows) {
  const { added, removed, changed, total } = diffSummary(rows);
  if (total === 0) return 'steps unchanged';
  return [added && `+${added}`, removed && `−${removed}`, changed && `~${changed}`].filter(Boolean).join(' ');
}

function VersionRow({ projectId, testId, version, previous, current, editable, onRestore }) {
  const [open, setOpen] = React.useState(false);
  const [detail, setDetail] = React.useState(null);

  const toggle = async () => {
    setOpen((value) => !value);
    if (detail) return;
    const versions = [version.version, previous?.version].filter(Boolean);
    const loaded = await Promise.all(versions.map((number) => testApi.version(projectId, testId, number)));
    setDetail({ steps: loaded[0].steps, previous: loaded[1]?.steps || [] });
  };

  return (
    <div className={css({ borderBottom: `1px solid ${T.borderDivider}` })}>
      <button
        type="button"
        onClick={toggle}
        className={css({
          display: 'flex',
          alignItems: 'center',
          gap: 12,
          width: '100%',
          padding: '13px 4px',
          border: 'none',
          background: 'transparent',
          cursor: 'pointer',
          fontFamily: fonts.body,
          textAlign: 'left',
        })}
      >
        <span className={css({ fontFamily: fonts.mono, fontSize: 13, fontWeight: 600, width: 36 })}>
          v{version.version}
        </span>
        <span className={css({ fontSize: 12, fontWeight: 600, color: T.brandText, width: 84 })}>
          {version.sourceLabel}
        </span>
        <span className={css({ flex: 1, fontSize: 13.5, color: T.textPrimary })}>{version.note || version.title}</span>
        <span className={css({ fontSize: 12, color: T.textTertiary })}>
          {version.stepCount} steps · {version.created}
        </span>
      </button>
      {open && (
        <div className={css({ padding: '0 4px 16px 52px' })}>
          {detail ? (
            <>
              <div className={css({ fontSize: 12, color: T.textTertiary, marginBottom: 8 })}>
                {previous
                  ? `Against v${previous.version}: ${summaryText(diffSteps(detail.previous, detail.steps))}`
                  : 'First version'}
              </div>
              <DiffRows before={detail.previous} after={detail.steps} />
              {editable && !current && (
                <div className={css({ marginTop: 12 })}>
                  <Button variant="secondary" size="sm" onClick={() => onRestore(version.version)}>
                    Restore v{version.version}
                  </Button>
                </div>
              )}
            </>
          ) : (
            <Spinner />
          )}
        </div>
      )}
    </div>
  );
}

function VersionHistory({ projectId, test, editable, onRestored }) {
  const [versions, setVersions] = React.useState(null);
  const [error, setError] = React.useState(null);

  React.useEffect(() => {
    testApi.versions(projectId, test.id).then(setVersions).catch(setError);
  }, [projectId, test.id, test.version]);

  const restore = async (number) => {
    setError(null);
    try {
      onRestored(await testApi.restore(projectId, test.id, number));
    } catch (err) {
      setError(err);
    }
  };

  return (
    <Card padding={18}>
      <ErrorNote error={error} />
      {versions === null ? (
        <Spinner />
      ) : (
        versions.map((version, index) => (
          <VersionRow
            key={version.version}
            projectId={projectId}
            testId={test.id}
            version={version}
            previous={versions[index + 1]}
            current={version.version === test.version}
            editable={editable}
            onRestore={restore}
          />
        ))
      )}
    </Card>
  );
}

export { DiffRows, VersionHistory };
