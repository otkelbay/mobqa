// Reusable visual primitives for the MobQA redesign.
import React from 'react';
import { T, fonts, radii } from '../../styles/tokens.js';
import { css, cx } from '../../styles/styleRegistry.js';
import {
  buttonStyle,
  cardStyle,
  inputStyle,
  pageContainerStyle,
  pageHeaderStyle,
  pageSubtitleStyle,
  pageTitleStyle,
  selectStyle,
  textareaStyle,
} from '../../styles/recipes.js';

// ── Spinner ────────────────────────────────────────────────────────────────
function Spinner({ size = 14, color = T.brand, track = T.brandBorder }) {
  return (
    <span
      className={css({
        width: size,
        height: size,
        border: `2px solid ${track}`,
        borderTopColor: color,
        borderRadius: '50%',
        display: 'inline-block',
        animation: 'mq-spin 0.7s linear infinite',
        flexShrink: 0,
      })}
    />
  );
}

// ── Avatar (initials) ────────────────────────────────────────────────────────
function Avatar({ initials, size = 34, bg = T.avatarBg, color = T.brandText, square = false }) {
  return (
    <span
      className={css({
        width: size,
        height: size,
        borderRadius: square ? Math.round(size * 0.27) : '50%',
        background: bg,
        color,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        fontSize: Math.max(11, Math.round(size * 0.38)),
        fontWeight: 600,
        fontFamily: fonts.body,
        flexShrink: 0,
      })}
    >
      {initials}
    </span>
  );
}

// ── Icon chip (rounded-square icon tile) ─────────────────────────────────────
function IconChip({ children, size = 30, bg = T.brandSubtle, color = T.brandText, radius }) {
  return (
    <span
      className={css({
        width: size,
        height: size,
        borderRadius: radius ?? Math.round(size * 0.27),
        background: bg,
        color,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        flexShrink: 0,
      })}
    >
      {children}
    </span>
  );
}

// ── Segmented control / tabs ─────────────────────────────────────────────────
// items: [{ value, label, icon?, hint?, dot? }]
function SegmentedTabs({ items, value, onChange, variant = 'pill' }) {
  const container =
    variant === 'card'
      ? {
          display: 'flex',
          gap: 6,
          background: T.bgSurface,
          border: `1px solid ${T.borderDefault}`,
          borderRadius: 12,
          padding: 5,
        }
      : { display: 'inline-flex', background: '#F1F1F4', borderRadius: 11, padding: 3, gap: 2 };
  return (
    <div className={css(container)}>
      {items.map((it) => {
        const active = it.value === value;
        const btn =
          variant === 'card'
            ? {
                flex: 1,
                flexDirection: 'column',
                gap: 6,
                padding: 14,
                borderRadius: 9,
                background: active ? T.brandSubtle : 'transparent',
              }
            : {
                gap: 7,
                padding: '7px 13px',
                borderRadius: 9,
                background: active ? T.bgSurface : 'transparent',
                boxShadow: active ? '0 1px 2px rgba(28,28,36,0.06)' : 'none',
              };
        return (
          <button
            key={it.value}
            type="button"
            onClick={() => onChange(it.value)}
            className={css({
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              border: 'none',
              cursor: 'pointer',
              fontFamily: fonts.body,
              fontSize: 13.5,
              fontWeight: 600,
              color: active ? T.brandText : T.textSecondary,
              transition: 'background 120ms, color 120ms',
              ...btn,
            })}
          >
            {it.icon}
            {it.label}
            {it.hint && <span className={css({ fontWeight: 400, color: T.textFaint, fontSize: 11.5 })}>{it.hint}</span>}
            {it.dot && <span className={css({ width: 7, height: 7, borderRadius: '50%', background: T.danger })} />}
          </button>
        );
      })}
    </div>
  );
}

// ── Dropzone ─────────────────────────────────────────────────────────────────
function Dropzone({ icon, title, subtitle, action, onClick, inputRef, onFiles, accept, multiple }) {
  const [over, setOver] = React.useState(false);
  const handleDrop = (e) => {
    e.preventDefault();
    setOver(false);
    if (onFiles && e.dataTransfer?.files?.length) onFiles(Array.from(e.dataTransfer.files));
  };
  return (
    <div
      onClick={onClick}
      onDragOver={(e) => {
        e.preventDefault();
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={handleDrop}
      className={css({
        border: `1.5px dashed ${over ? T.brand : T.brandBorder}`,
        background: over ? '#F6F4FF' : T.bgDropzone,
        borderRadius: 14,
        padding: 40,
        textAlign: 'center',
        cursor: onClick ? 'pointer' : 'default',
        transition: 'border-color 120ms, background 120ms',
      })}
    >
      {inputRef && (
        <input
          ref={inputRef}
          type="file"
          accept={accept}
          multiple={multiple}
          className={css({ display: 'none' })}
          onChange={(e) => onFiles && onFiles(Array.from(e.target.files || []))}
        />
      )}
      <span
        className={css({
          width: 54,
          height: 54,
          borderRadius: 14,
          background: T.brandSubtle,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          margin: '0 auto 14px',
          color: T.brand,
        })}
      >
        {icon}
      </span>
      <div className={css({ fontSize: 15, fontWeight: 600, color: T.textPrimary })}>{title}</div>
      {subtitle && <div className={css({ fontSize: 13, color: T.textTertiary, marginTop: 6 })}>{subtitle}</div>}
      {action && (
        <div
          className={css({
            display: 'inline-block',
            marginTop: 16,
            padding: '9px 16px',
            background: '#fff',
            border: `1px solid ${T.brandTintAlt}`,
            borderRadius: 9,
            fontSize: 13,
            fontWeight: 600,
            color: T.brandText,
          })}
        >
          {action}
        </div>
      )}
    </div>
  );
}

// ── List row (hover-aware) ───────────────────────────────────────────────────
function ListRow({ children, onClick, radius = 13, padding = '16px 18px', gap = 16 }) {
  const [hov, setHov] = React.useState(false);
  const clickable = !!onClick;
  return (
    <button
      type="button"
      onClick={onClick}
      onMouseEnter={() => setHov(true)}
      onMouseLeave={() => setHov(false)}
      className={css({
        display: 'flex',
        alignItems: 'center',
        gap,
        width: '100%',
        textAlign: 'left',
        background: T.bgSurface,
        border: `1px solid ${hov && clickable ? T.brandBorder : T.borderDefault}`,
        borderRadius: radius,
        padding,
        cursor: clickable ? 'pointer' : 'default',
        fontFamily: fonts.body,
        transition: 'border-color 120ms',
      })}
    >
      {children}
    </button>
  );
}

// ── Empty state ──────────────────────────────────────────────────────────────
function EmptyState({ icon, title, subtitle, action }) {
  return (
    <div
      className={css({
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        gap: 10,
        textAlign: 'center',
        padding: '56px 24px',
        background: T.bgSurface,
        border: `1px solid ${T.borderDefault}`,
        borderRadius: radii.card,
      })}
    >
      {icon && <div className={css({ color: T.textTertiary })}>{icon}</div>}
      <div className={css({ fontSize: 15, fontWeight: 600, color: T.textPrimary })}>{title}</div>
      {subtitle && <div className={css({ fontSize: 13, color: T.textTertiary, maxWidth: 360 })}>{subtitle}</div>}
      {action && <div className={css({ marginTop: 6 })}>{action}</div>}
    </div>
  );
}

// ── Section label (uppercase eyebrow) ────────────────────────────────────────
function SectionLabel({ children, className }) {
  return (
    <div
      className={cx(
        css({
          fontSize: 12,
          fontWeight: 600,
          letterSpacing: '0.05em',
          color: T.textFaint,
          marginBottom: 12,
        }),
        className,
      )}
    >
      {children}
    </div>
  );
}

// ── Buttons and form controls ────────────────────────────────────────────────
function Button({ variant = 'primary', size = 'md', disabled, busy, children, type = 'button', ...rest }) {
  const [hover, setHover] = React.useState(false);
  const off = disabled || busy;
  return (
    <button
      type={type}
      disabled={off}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      className={css(buttonStyle({ variant, size, hover: hover && !off, disabled: off }))}
      {...rest}
    >
      {busy && <Spinner size={13} color="currentColor" track="transparent" />}
      {children}
    </button>
  );
}

// Field labels one control; with group it titles several controls, which a <label> must not contain.
function Field({ label, hint, group = false, children }) {
  const Tag = group ? 'div' : 'label';
  return (
    <Tag
      role={group ? 'group' : undefined}
      aria-label={group ? label : undefined}
      className={css({ display: 'block', marginBottom: 16 })}
    >
      <span className={css({ display: 'block', fontSize: 13, fontWeight: 600, color: T.textMedium, marginBottom: 7 })}>
        {label}
      </span>
      {children}
      {hint && (
        <span className={css({ display: 'block', fontSize: 12, color: T.textTertiary, marginTop: 6 })}>{hint}</span>
      )}
    </Tag>
  );
}

function TextInput({ style, ...rest }) {
  const [focused, setFocused] = React.useState(false);
  return (
    <input
      onFocus={() => setFocused(true)}
      onBlur={() => setFocused(false)}
      className={css(inputStyle({ focused, extraStyle: style }))}
      {...rest}
    />
  );
}

function TextArea({ style, ...rest }) {
  const [focused, setFocused] = React.useState(false);
  return (
    <textarea
      onFocus={() => setFocused(true)}
      onBlur={() => setFocused(false)}
      className={css(textareaStyle({ focused, extraStyle: style }))}
      {...rest}
    />
  );
}

function Select({ options, style, ...rest }) {
  return (
    <select className={css(selectStyle({ width: '100%', ...style }))} {...rest}>
      {options.map((option) => (
        <option key={option.value} value={option.value}>
          {option.label}
        </option>
      ))}
    </select>
  );
}

// ── Page layout ──────────────────────────────────────────────────────────────
function Page({ title, subtitle, actions, back, width, children }) {
  return (
    <div className={css({ ...pageContainerStyle, ...(width ? { maxWidth: width } : {}) })}>
      {back}
      <div className={css(pageHeaderStyle)}>
        <div className={css({ minWidth: 0 })}>
          <h1 className={css(pageTitleStyle)}>{title}</h1>
          {subtitle && <p className={css(pageSubtitleStyle)}>{subtitle}</p>}
        </div>
        {actions && <div className={css({ display: 'flex', gap: 10, flexShrink: 0 })}>{actions}</div>}
      </div>
      {children}
    </div>
  );
}

function BackLink({ onClick, children }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={css({
        background: 'none',
        border: 'none',
        padding: 0,
        marginBottom: 18,
        cursor: 'pointer',
        fontFamily: fonts.body,
        fontSize: 13,
        fontWeight: 500,
        color: T.textSecondary,
      })}
    >
      ← {children}
    </button>
  );
}

function Card({ children, padding = 22, style }) {
  return <div className={css(cardStyle({ extraStyle: { padding, marginBottom: 18, ...style } }))}>{children}</div>;
}

function ErrorNote({ error }) {
  if (!error) return null;
  return (
    <div
      className={css({
        background: T.dangerSubtle,
        color: T.danger,
        borderRadius: 10,
        padding: '10px 14px',
        fontSize: 13,
        marginBottom: 16,
      })}
    >
      {error.message || String(error)}
    </div>
  );
}

export {
  Avatar,
  BackLink,
  Button,
  Card,
  Dropzone,
  EmptyState,
  ErrorNote,
  Field,
  IconChip,
  ListRow,
  Page,
  SectionLabel,
  SegmentedTabs,
  Select,
  Spinner,
  TextArea,
  TextInput,
};
