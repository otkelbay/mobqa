// A test step is plain language: an action, the element or value it works on, and what should happen.
// The authoring agent turns steps into a script on a real device, so the editor only has to keep them
// clear, not machine-precise.

const STEP_ACTIONS = [
  { value: 'tap', label: 'Tap', target: 'Element, e.g. "Sign in button"', bg: '#EFF4FF', color: '#1D4ED8' },
  {
    value: 'input',
    label: 'Enter text',
    target: 'Field and text, e.g. Email: {{LOGIN}}',
    bg: '#F0FDF4',
    color: '#16A34A',
  },
  { value: 'assert', label: 'Check', target: 'What must be visible', bg: '#FFFBEB', color: '#D97706' },
  { value: 'wait', label: 'Wait', target: 'How long, e.g. 2 seconds', bg: '#F3F4F6', color: '#6B7280' },
  { value: 'swipe', label: 'Swipe', target: 'Direction or element', bg: '#F5F3FF', color: '#7C3AED' },
  { value: 'scroll', label: 'Scroll', target: 'Direction', bg: '#FFF1F2', color: '#E11D48' },
  { value: 'back', label: 'Back', target: '', bg: '#F3F4F6', color: '#4B5563' },
  { value: 'long_press', label: 'Long press', target: 'Element', bg: '#EFF4FF', color: '#1E40AF' },
  { value: 'double_tap', label: 'Double tap', target: 'Element', bg: '#EFF4FF', color: '#2563EB' },
  { value: 'clear_text', label: 'Clear field', target: 'Field', bg: '#F0FDF4', color: '#15803D' },
  { value: 'hide_keyboard', label: 'Hide keyboard', target: '', bg: '#F3F4F6', color: '#374151' },
  {
    value: 'scroll_until_visible',
    label: 'Scroll to element',
    target: 'Element to bring into view',
    bg: '#FFF1F2',
    color: '#BE123C',
  },
  { value: 'wait_for_element', label: 'Wait for element', target: 'Element', bg: '#F3F4F6', color: '#4B5563' },
  { value: 'open_deeplink', label: 'Deep link', target: 'myapp://orders/42', bg: '#EEF2FF', color: '#4F46E5' },
  { value: 'assert_count', label: 'Count', target: 'Elements and how many', bg: '#FFFBEB', color: '#B45309' },
  { value: 'screenshot', label: 'Screenshot', target: '', bg: '#ECFEFF', color: '#0891B2' },
  { value: 'call', label: 'API action', target: 'Name of an API action', bg: '#F0FDFA', color: '#0F766E' },
];

const PRIORITIES = ['critical', 'high', 'medium', 'low'].map((value) => ({
  value,
  label: value[0].toUpperCase() + value.slice(1),
}));

const ACTION_BY_VALUE = new Map(STEP_ACTIONS.map((action) => [action.value, action]));
const FALLBACK_COLOR = { bg: '#F3F4F6', color: '#4B5563' };

let localStepKey = 0;

function nextStepId() {
  localStepKey += 1;
  return `step-${Date.now()}-${localStepKey}`;
}

function createEmptyStep(overrides = {}) {
  return { id: nextStepId(), action: 'tap', target: '', description: '', expectedResult: '', ...overrides };
}

function stepFromApi(step = {}, id = nextStepId()) {
  return {
    id,
    action: ACTION_BY_VALUE.has(step.action) ? step.action : 'tap',
    target: step.target || '',
    description: step.description || '',
    expectedResult: step.expected_result || '',
  };
}

function stepToApi(step) {
  return {
    action: step.action,
    target: step.target.trim(),
    description: step.description.trim(),
    expected_result: step.expectedResult.trim(),
  };
}

function actionLabel(action) {
  return ACTION_BY_VALUE.get(action)?.label || action;
}

function actionColor(action) {
  const known = ACTION_BY_VALUE.get(action);
  return known ? { bg: known.bg, color: known.color } : FALLBACK_COLOR;
}

function targetPlaceholder(action) {
  return ACTION_BY_VALUE.get(action)?.target || '';
}

function actionTakesTarget(action) {
  return targetPlaceholder(action) !== '';
}

function stepSummary(step) {
  const target = step.target.trim();
  return target ? `${actionLabel(step.action)} · ${target}` : actionLabel(step.action);
}

function parseTags(text) {
  return [
    ...new Set(
      text
        .split(',')
        .map((tag) => tag.trim().toLowerCase())
        .filter(Boolean),
    ),
  ];
}

function validationIssueForStep(step, index) {
  if (step.action === 'call' && !step.target.trim()) {
    return `Step ${index + 1}: name the API action to call`;
  }
  if (actionTakesTarget(step.action) && !step.target.trim() && !step.description.trim()) {
    return `Step ${index + 1}: say what to ${actionLabel(step.action).toLowerCase()}`;
  }
  return '';
}

export {
  PRIORITIES,
  STEP_ACTIONS,
  actionColor,
  actionLabel,
  actionTakesTarget,
  createEmptyStep,
  parseTags,
  stepFromApi,
  stepSummary,
  stepToApi,
  targetPlaceholder,
  validationIssueForStep,
};
