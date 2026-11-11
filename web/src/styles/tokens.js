// Design tokens — MobQA redesign (purple / Onest).
// Backward-compatible keys (brand, bgBase, textPrimary, …) keep their names so
// existing pages pick up the new palette automatically; new keys extend it.
const T = {
  // Backgrounds
  bgBase: '#F4F4F6', // page background
  bgSurface: '#FFFFFF', // cards, panels, sidebar
  bgSubtle: '#FAFAFB', // subtle fills (stat boxes, inputs)
  bgFaint: '#F0F0F3', // slightly darker fill / muted chip
  bgElevated: '#FFFFFF',
  bgDropzone: '#FBFAFF', // dashed dropzones / agent banners

  // Text
  textPrimary: '#1C1C24',
  heading: '#16161C',
  textMedium: '#43434E',
  textSecondary: '#8A8A95',
  textMuted: '#6B6B76',
  textTertiary: '#9A9AA5',
  textFaint: '#B3B3BD',
  iconGray: '#B6B6C0',

  // Brand / accent (purple)
  brand: '#7157FB',
  brandHover: '#5E43F0',
  brandText: '#5E43F0',
  brandSubtle: '#F1EEFE',
  brandTint: '#E9E3FD',
  brandTintAlt: '#E2DBFE',
  brandBorder: '#D9D1FD',
  brandBannerBorder: '#EFEBFD',
  avatarBg: '#EDEAFE',

  // Status
  success: '#1A9E5E',
  successSubtle: '#E9F6EF',
  warning: '#C98A1E',
  warningSubtle: '#FAF1DF',
  danger: '#D9483B',
  dangerSubtle: '#FCF1EF',
  info: '#3A6FB0',
  neutral: '#6B6B76',
  neutralSubtle: '#F4F4F6',

  // Borders
  borderDefault: '#ECECF0',
  borderLight: '#F0F0F3',
  borderDivider: '#F4F4F6',
  borderStrong: '#D9D1FD', // hover / emphasis border (purple)
  borderFocus: '#D9D1FD',

  // Chat
  chatAgentBg: '#F4F2FF',
  chatAgentText: '#3F3457',
  chatUserBg: '#7157FB',
};

const fonts = {
  body: "'Onest', sans-serif",
  display: "'Onest', sans-serif",
  mono: "'JetBrains Mono', monospace",
  onest: 'Onest',
  jetbrains: 'JetBrains Mono',
  // Back-compat aliases (old code referenced Inter/Manrope) → now Onest.
  inter: 'Onest',
  manrope: 'Onest',
};

const shadows = {
  cardHover: '0 6px 22px rgba(28,28,36,0.06)',
  menu: '0 6px 24px rgba(28,28,36,0.10)',
  toast: '0 4px 16px rgba(28,28,36,0.10)',
  sheet: '0 8px 32px rgba(28,28,36,0.12)',
  btn: '0 1px 2px rgba(113,87,251,0.35)',
  btnLg: '0 2px 6px rgba(113,87,251,0.32)',
};

const radii = {
  xs: 6,
  sm: 7,
  md: 10,
  input: 11,
  lg: 11,
  xl: 14,
  card: 16,
  xxl: 20,
  pill: 9999,
};

export { T, fonts, radii, shadows };
