import { describe, expect, it } from 'vitest';
import { diffSteps, diffSummary, stepSignature } from './stepDiff.js';

const step = (over = {}) => ({ action: 'tap', target: 'Sign in', description: '', expectedResult: '', ...over });

describe('diffSteps', () => {
  it('marks identical lists as all same', () => {
    const steps = [step(), step({ action: 'input', target: 'Email: {{LOGIN}}' })];
    const rows = diffSteps(steps, steps);
    expect(rows.map((r) => r.type)).toEqual(['same', 'same']);
    expect(diffSummary(rows).total).toBe(0);
  });

  it('detects an inserted step and keeps neighbours same', () => {
    const oldSteps = [step({ target: 'A' }), step({ target: 'B' })];
    const newSteps = [oldSteps[0], step({ action: 'wait', target: '2 seconds' }), oldSteps[1]];
    expect(diffSteps(oldSteps, newSteps).map((r) => r.type)).toEqual(['same', 'added', 'same']);
  });

  it('detects a removed step', () => {
    const oldSteps = [step({ target: 'A' }), step({ target: 'B' })];
    expect(diffSteps(oldSteps, [oldSteps[0]]).map((r) => r.type)).toEqual(['same', 'removed']);
  });

  it('pairs a replacement into a changed row with old and new', () => {
    const oldSteps = [step({ target: 'A' }), step({ action: 'input', target: 'Promo code: PROMO10' })];
    const newSteps = [oldSteps[0], step({ action: 'input', target: 'Promo code: WELCOME20' })];
    const rows = diffSteps(oldSteps, newSteps);
    expect(rows.map((r) => r.type)).toEqual(['same', 'changed']);
    expect(rows[1].oldStep.target).toBe('Promo code: PROMO10');
    expect(rows[1].step.target).toBe('Promo code: WELCOME20');
    expect(diffSummary(rows)).toMatchObject({ added: 0, removed: 0, changed: 1, total: 1 });
  });

  it('treats an expected result change as a change', () => {
    const rows = diffSteps([step()], [step({ expectedResult: 'Home screen opens' })]);
    expect(rows.map((r) => r.type)).toEqual(['changed']);
  });
});

describe('stepSignature', () => {
  it('ignores case, surrounding spaces and local ids', () => {
    expect(stepSignature({ id: 'a', ...step({ target: ' Sign In ' }) })).toBe(
      stepSignature({ id: 'b', ...step({ target: 'sign in' }) }),
    );
  });
});
