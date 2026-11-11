import { describe, expect, it } from 'vitest';
import { formatRelative, mapDevice, mapRun, mapVersion, platformLabel, scriptState } from './viewModelMappers.js';

describe('scriptState', () => {
  const test = { version: 3 };

  it('is none without a succeeded authoring', () => {
    expect(scriptState(test, [])).toBe('none');
    expect(scriptState(test, [{ status: 'failed', testVersion: 3 }])).toBe('none');
  });

  it('is authoring while one is queued or running', () => {
    expect(scriptState(test, [{ status: 'running' }, { status: 'succeeded', testVersion: 3 }])).toBe('authoring');
  });

  it('compares the newest succeeded script with the test version', () => {
    expect(scriptState(test, [{ status: 'succeeded', testVersion: 3 }])).toBe('current');
    expect(
      scriptState(test, [
        { status: 'succeeded', testVersion: 2 },
        { status: 'succeeded', testVersion: 3 },
      ]),
    ).toBe('stale');
  });
});

describe('formatRelative', () => {
  const now = Date.parse('2026-11-11T12:00:00Z');

  it('rounds to minutes, hours and days', () => {
    expect(formatRelative('2026-11-11T11:59:50Z', now)).toBe('1 min ago');
    expect(formatRelative('2026-11-11T09:00:00Z', now)).toBe('3 h ago');
    expect(formatRelative('2026-11-10T12:00:00Z', now)).toBe('1 day ago');
    expect(formatRelative('2026-11-07T12:00:00Z', now)).toBe('4 days ago');
  });

  it('shows a dash for a missing or broken date', () => {
    expect(formatRelative(null, now)).toBe('—');
    expect(formatRelative('not a date', now)).toBe('—');
  });
});

describe('mappers', () => {
  it('names a device after its model when it has no name', () => {
    const device = mapDevice({ id: 'd1', platform: 'ios', model: 'iPhone 15', os_version: '18.1', serial: 'x' });
    expect(device).toMatchObject({ name: 'iPhone 15', os: 'iOS 18.1' });
  });

  it('measures a run from start to finish', () => {
    const run = mapRun({
      id: 'r1',
      status: 'passed',
      summary: {},
      created_at: '2026-11-11T12:00:00Z',
      started_at: '2026-11-11T12:00:00Z',
      finished_at: '2026-11-11T12:01:05Z',
    });
    expect(run.duration).toBe('1 min 5 s');
    expect(run.buildLabel).toBe('');
  });

  it('labels the first manual version as created', () => {
    expect(mapVersion({ version: 1, source: 'manual' }).sourceLabel).toBe('Created');
    expect(mapVersion({ version: 2, source: 'manual' }).sourceLabel).toBe('Edited');
    expect(mapVersion({ version: 1, source: 'generate' }).sourceLabel).toBe('Generated');
  });

  it('keeps an unknown platform as is', () => {
    expect(platformLabel('android')).toBe('Android');
    expect(platformLabel('web')).toBe('web');
  });
});
