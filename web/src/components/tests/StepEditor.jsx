import React from 'react';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import {
  STEP_ACTIONS,
  actionColor,
  actionLabel,
  actionTakesTarget,
  createEmptyStep,
  targetPlaceholder,
  validationIssueForStep,
} from '../../services/testStepModel.js';
import { Button, Select, TextInput } from '../ui/primitives.jsx';

const compact = { padding: '9px 11px', fontSize: 13 };

function ActionChip({ action, children }) {
  const { bg, color } = actionColor(action);
  return (
    <span
      className={css({
        fontSize: 11.5,
        fontWeight: 600,
        padding: '3px 8px',
        borderRadius: 6,
        background: bg,
        color,
        flexShrink: 0,
      })}
    >
      {children}
    </span>
  );
}

function StepRow({ index, step, total, onChange, onMove, onRemove }) {
  const set = (key) => (e) => onChange({ ...step, [key]: e.target.value });
  return (
    <div
      className={css({
        display: 'grid',
        gridTemplateColumns: '28px 170px 1fr auto',
        gap: 8,
        alignItems: 'start',
        padding: '12px 0',
        borderBottom: `1px solid ${T.borderDivider}`,
      })}
    >
      <span className={css({ fontFamily: fonts.mono, fontSize: 12.5, color: T.textTertiary, paddingTop: 9 })}>
        {index + 1}
      </span>
      <Select options={STEP_ACTIONS} value={step.action} onChange={set('action')} style={compact} />
      <div className={css({ display: 'flex', flexDirection: 'column', gap: 6 })}>
        {actionTakesTarget(step.action) && (
          <TextInput
            value={step.target}
            onChange={set('target')}
            placeholder={targetPlaceholder(step.action)}
            style={compact}
          />
        )}
        <TextInput
          value={step.description}
          onChange={set('description')}
          placeholder="Notes for the agent (optional)"
          style={compact}
        />
        <TextInput
          value={step.expectedResult}
          onChange={set('expectedResult')}
          placeholder="Expected result (optional)"
          style={compact}
        />
      </div>
      <div className={css({ display: 'flex', gap: 2 })}>
        <Button variant="ghost" size="sm" onClick={() => onMove(-1)} disabled={index === 0} aria-label="Move up">
          ↑
        </Button>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => onMove(1)}
          disabled={index === total - 1}
          aria-label="Move down"
        >
          ↓
        </Button>
        <Button variant="ghost" size="sm" onClick={onRemove} aria-label="Remove step">
          ×
        </Button>
      </div>
    </div>
  );
}

function StepListEditor({ steps, onChange }) {
  const replace = (index, next) => onChange(steps.map((step, i) => (i === index ? next : step)));
  const move = (index, delta) => {
    const next = [...steps];
    [next[index], next[index + delta]] = [next[index + delta], next[index]];
    onChange(next);
  };
  return (
    <div>
      {steps.map((step, index) => (
        <StepRow
          key={step.id}
          index={index}
          step={step}
          total={steps.length}
          onChange={(next) => replace(index, next)}
          onMove={(delta) => move(index, delta)}
          onRemove={() => onChange(steps.filter((_, i) => i !== index))}
        />
      ))}
      <div className={css({ marginTop: 12 })}>
        <Button variant="secondary" size="sm" onClick={() => onChange([...steps, createEmptyStep()])}>
          Add step
        </Button>
      </div>
    </div>
  );
}

function StepList({ steps }) {
  return (
    <ol className={css({ listStyle: 'none', margin: 0, padding: 0 })}>
      {steps.map((step, index) => (
        <li
          key={step.id}
          className={css({
            display: 'grid',
            gridTemplateColumns: '28px 1fr',
            gap: 8,
            padding: '11px 0',
            borderBottom: `1px solid ${T.borderDivider}`,
          })}
        >
          <span className={css({ fontFamily: fonts.mono, fontSize: 12.5, color: T.textTertiary })}>{index + 1}</span>
          <span>
            <span className={css({ display: 'flex', alignItems: 'center', gap: 8 })}>
              <ActionChip action={step.action}>{actionLabel(step.action)}</ActionChip>
              <span className={css({ fontSize: 13.5, color: T.textPrimary })}>{step.target}</span>
            </span>
            {step.description && (
              <span className={css({ display: 'block', fontSize: 12.5, color: T.textSecondary, marginTop: 4 })}>
                {step.description}
              </span>
            )}
            {step.expectedResult && (
              <span className={css({ display: 'block', fontSize: 12.5, color: T.success, marginTop: 4 })}>
                → {step.expectedResult}
              </span>
            )}
          </span>
        </li>
      ))}
    </ol>
  );
}

function stepsValidationError(steps) {
  if (steps.length === 0) return 'Add at least one step';
  return steps.map(validationIssueForStep).find(Boolean) || '';
}

export { ActionChip, StepList, StepListEditor, stepsValidationError };
