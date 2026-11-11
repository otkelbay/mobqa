import { T, fonts, radii, shadows } from './tokens.js';

const controlPadding = { sm: '9px 14px', md: '11px 17px', lg: '12px 18px' };
const controlFontSize = { sm: 13, md: 13.5, lg: 14 };

const statusConfig = {
  queued: { bg: T.neutralSubtle, color: T.textMuted, dot: T.textTertiary, label: 'Queued' },
  pending: { bg: T.neutralSubtle, color: T.textMuted, dot: T.textTertiary, label: 'Pending' },
  running: { bg: T.brandSubtle, color: T.brandText, dot: T.brand, label: 'Running' },
  passed: { bg: T.successSubtle, color: T.success, dot: T.success, label: 'Passed' },
  succeeded: { bg: T.successSubtle, color: T.success, dot: T.success, label: 'Succeeded' },
  flaky: { bg: T.warningSubtle, color: T.warning, dot: T.warning, label: 'Flaky' },
  failed: { bg: T.dangerSubtle, color: T.danger, dot: T.danger, label: 'Failed' },
  error: { bg: T.dangerSubtle, color: T.danger, dot: T.danger, label: 'Error' },
  skipped: { bg: T.neutralSubtle, color: T.textMuted, dot: T.textTertiary, label: 'Skipped' },
  cancelled: { bg: T.neutralSubtle, color: T.textMuted, dot: T.textTertiary, label: 'Cancelled' },
  authoring: { bg: T.brandSubtle, color: T.brandText, dot: T.brand, label: 'Authoring' },
  none: { bg: T.warningSubtle, color: T.warning, dot: T.warning, label: 'No script' },
  current: { bg: T.successSubtle, color: T.success, dot: T.success, label: 'Script ready' },
  stale: { bg: T.warningSubtle, color: T.warning, dot: T.warning, label: 'Script outdated' },
};

// Filled status pill, matching the redesign.
function statusBadgeStyle(config, size) {
  return {
    display: 'inline-flex',
    alignItems: 'center',
    gap: 6,
    background: config.bg,
    color: config.color,
    padding: size === 'sm' ? '4px 9px' : '5px 11px',
    borderRadius: 8,
    fontSize: size === 'sm' ? 11.5 : 12,
    lineHeight: 1.3,
    fontWeight: 600,
    fontFamily: fonts.body,
    whiteSpace: 'nowrap',
    // A badge sits next to variable-length text in list rows; it must keep its
    // own width rather than be squeezed by a long neighbour.
    flexShrink: 0,
  };
}

function badgeDotStyle(config, running) {
  return {
    width: 7,
    height: 7,
    borderRadius: '50%',
    background: config.dot,
    flexShrink: 0,
    ...(running ? { animation: 'mq-pulse 1.5s infinite' } : {}),
  };
}

// Small inline metadata chip ("video · 12 steps" style label).
function platformBadgeStyle() {
  return {
    display: 'inline-flex',
    alignItems: 'center',
    gap: 4,
    background: T.borderDivider,
    color: T.textMuted,
    padding: '3px 9px',
    borderRadius: 7,
    fontSize: 12,
    fontWeight: 500,
    fontFamily: fonts.body,
  };
}

function buttonStyle({ variant, size, hover, active, disabled }) {
  const base = {
    display: 'inline-flex',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 8,
    padding: controlPadding[size] || controlPadding.md,
    borderRadius: radii.lg,
    fontSize: controlFontSize[size] || controlFontSize.md,
    fontWeight: 600,
    fontFamily: fonts.body,
    cursor: disabled ? 'not-allowed' : 'pointer',
    border: '1px solid transparent',
    outline: 'none',
    transition: 'background 120ms, border-color 120ms, color 120ms, transform 80ms',
    transform: active ? 'scale(0.98)' : 'scale(1)',
    opacity: disabled ? 0.55 : 1,
    whiteSpace: 'nowrap',
  };
  const variants = {
    primary: {
      background: hover ? T.brandHover : T.brand,
      color: '#fff',
      boxShadow: shadows.btn,
    },
    secondary: {
      background: '#fff',
      color: hover ? T.brandText : T.textMedium,
      borderColor: hover ? T.brandBorder : T.borderDefault,
    },
    soft: {
      background: hover ? T.brandTint : T.brandSubtle,
      color: T.brandText,
      borderColor: T.brandTintAlt,
    },
    danger: { background: T.danger, color: '#fff' },
    dangerOutline: {
      background: '#fff',
      color: T.danger,
      borderColor: hover ? T.danger : T.dangerSubtle,
    },
    ghost: {
      background: hover ? T.bgSubtle : 'transparent',
      color: hover ? T.brandText : T.textSecondary,
    },
  };
  return { ...base, ...(variants[variant] || variants.primary) };
}

function cardStyle({ hover, clickable, extraStyle }) {
  return {
    background: T.bgSurface,
    borderRadius: radii.card,
    border: `1px solid ${hover && clickable ? T.brandBorder : T.borderDefault}`,
    boxShadow: hover && clickable ? shadows.cardHover : 'none',
    cursor: clickable ? 'pointer' : 'default',
    transition: 'border-color 120ms, box-shadow 120ms',
    ...extraStyle,
  };
}

function inputStyle({ focused, extraStyle }) {
  return {
    border: `1px solid ${focused ? T.brandBorder : T.borderDefault}`,
    borderRadius: radii.input,
    padding: '13px 14px',
    fontSize: 14,
    fontFamily: fonts.body,
    outline: 'none',
    background: T.bgSubtle,
    color: T.textPrimary,
    width: '100%',
    boxSizing: 'border-box',
    ...extraStyle,
  };
}

function textareaStyle({ focused, extraStyle }) {
  return {
    ...inputStyle({ focused }),
    lineHeight: 1.6,
    resize: 'vertical',
    ...extraStyle,
  };
}

function selectStyle(extraStyle) {
  return {
    border: `1px solid ${T.borderDefault}`,
    borderRadius: radii.input,
    padding: '12px 34px 12px 14px',
    fontSize: 14,
    fontFamily: fonts.body,
    outline: 'none',
    background: T.bgSubtle,
    color: T.textPrimary,
    cursor: 'pointer',
    appearance: 'none',
    backgroundImage: `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='8' viewBox='0 0 12 8'%3E%3Cpath d='M1 1l5 5 5-5' stroke='%239A9AA5' stroke-width='1.5' fill='none' stroke-linecap='round'/%3E%3C/svg%3E")`,
    backgroundRepeat: 'no-repeat',
    backgroundPosition: 'right 12px center',
    ...extraStyle,
  };
}

// Resource / test list row used across Tests, Runs, Uploads, Data, Secrets, APIs.
function listRowStyle({ hover, clickable }) {
  return {
    display: 'flex',
    alignItems: 'center',
    gap: 16,
    width: '100%',
    textAlign: 'left',
    background: T.bgSurface,
    border: `1px solid ${hover && clickable ? T.brandBorder : T.borderDefault}`,
    borderRadius: 13,
    padding: '16px 18px',
    cursor: clickable ? 'pointer' : 'default',
    fontFamily: fonts.body,
    transition: 'border-color 120ms',
  };
}

const pageContainerStyle = {
  maxWidth: 980,
  margin: '0 auto',
  padding: '40px 48px 80px',
};

const pageHeaderStyle = {
  display: 'flex',
  justifyContent: 'space-between',
  alignItems: 'flex-end',
  marginBottom: 28,
};

const pageTitleStyle = {
  fontFamily: fonts.display,
  fontWeight: 700,
  fontSize: 26,
  color: T.heading,
  margin: 0,
  letterSpacing: '-0.02em',
};

const pageSubtitleStyle = {
  fontFamily: fonts.body,
  fontSize: 14,
  color: T.textSecondary,
  margin: '7px 0 0',
  fontWeight: 400,
};

export {
  badgeDotStyle,
  buttonStyle,
  cardStyle,
  inputStyle,
  listRowStyle,
  pageContainerStyle,
  pageHeaderStyle,
  pageSubtitleStyle,
  pageTitleStyle,
  platformBadgeStyle,
  selectStyle,
  statusBadgeStyle,
  statusConfig,
  textareaStyle,
};
