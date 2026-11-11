import { describe, expect, it } from 'vitest';
import {
  actionTakesTarget,
  createEmptyStep,
  parseTags,
  stepFromApi,
  stepSummary,
  stepToApi,
  validationIssueForStep,
} from './testStepModel.js';

describe('step conversion', () => {
  it('round-trips a step through the API shape', () => {
    const step = stepFromApi({ action: 'input', target: 'Email: {{LOGIN}}', expected_result: 'Field filled' });
    expect(step).toMatchObject({ action: 'input', target: 'Email: {{LOGIN}}', expectedResult: 'Field filled' });
    expect(stepToApi({ ...step, target: '  Email: {{LOGIN}} ' })).toEqual({
      action: 'input',
      target: 'Email: {{LOGIN}}',
      description: '',
      expected_result: 'Field filled',
    });
  });

  it('falls back to tap for an unknown action', () => {
    expect(stepFromApi({ action: 'teleport' }).action).toBe('tap');
  });

  it('gives every new step its own id', () => {
    expect(createEmptyStep().id).not.toBe(createEmptyStep().id);
  });
});

describe('stepSummary', () => {
  it('names the action and its target', () => {
    expect(stepSummary(createEmptyStep({ target: 'Sign in button' }))).toBe('Tap · Sign in button');
    expect(stepSummary(createEmptyStep({ action: 'back' }))).toBe('Back');
  });
});

describe('validationIssueForStep', () => {
  it('requires a target or a description for actions that work on something', () => {
    expect(actionTakesTarget('tap')).toBe(true);
    expect(actionTakesTarget('back')).toBe(false);
    expect(validationIssueForStep(createEmptyStep(), 1)).toBe('Step 2: say what to tap');
    expect(validationIssueForStep(createEmptyStep({ description: 'The blue button' }), 0)).toBe('');
    expect(validationIssueForStep(createEmptyStep({ action: 'back' }), 0)).toBe('');
  });

  it('requires an API action name for a call even with a description', () => {
    const step = createEmptyStep({ action: 'call', description: 'Create an order' });
    expect(validationIssueForStep(step, 0)).toBe('Step 1: name the API action to call');
  });
});

describe('parseTags', () => {
  it('splits, trims, lowercases and deduplicates', () => {
    expect(parseTags(' Smoke, checkout,,smoke ,Login ')).toEqual(['smoke', 'checkout', 'login']);
    expect(parseTags('')).toEqual([]);
  });
});
