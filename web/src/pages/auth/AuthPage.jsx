import React from 'react';
import { useAppData } from '../../context/AppDataContext.jsx';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import { Button, ErrorNote, Field, IconChip, TextInput } from '../../components/ui/primitives.jsx';
import { IconLogo } from '../../components/ui/icons.jsx';

function AuthPage() {
  const { login, register } = useAppData();
  const [mode, setMode] = React.useState('login');
  const [name, setName] = React.useState('');
  const [email, setEmail] = React.useState('');
  const [password, setPassword] = React.useState('');
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);

  const handleSubmit = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (mode === 'login') {
        await login(email, password);
      } else {
        await register({ name: name.trim(), email, password });
      }
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <div
      className={css({
        minHeight: '100vh',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        background: `radial-gradient(1200px 600px at 50% -10%, ${T.brandSubtle} 0%, ${T.bgBase} 55%)`,
        fontFamily: fonts.body,
        padding: 24,
      })}
    >
      <div
        className={css({
          width: 392,
          background: T.bgSurface,
          border: `1px solid ${T.borderDefault}`,
          borderRadius: 20,
          padding: '34px 32px',
          boxShadow: '0 18px 50px rgba(28,28,36,0.08)',
        })}
      >
        <div className={css({ display: 'flex', flexDirection: 'column', alignItems: 'center', marginBottom: 28 })}>
          <div className={css({ display: 'flex', alignItems: 'center', gap: 9, marginBottom: 10 })}>
            <IconChip size={28} bg={T.brand} radius={8}>
              <IconLogo size={16} />
            </IconChip>
            <span className={css({ fontSize: 20, fontWeight: 700, letterSpacing: '-0.02em', color: T.textPrimary })}>
              mobqa
            </span>
          </div>
          <div className={css({ fontSize: 13.5, color: T.textSecondary })}>
            {mode === 'login' ? 'Sign in to your account' : 'Create an account'}
          </div>
        </div>

        <form onSubmit={handleSubmit}>
          <ErrorNote error={error} />
          {mode === 'register' && (
            <Field label="Name">
              <TextInput value={name} onChange={(e) => setName(e.target.value)} placeholder="Ada Lovelace" required />
            </Field>
          )}
          <Field label="Email">
            <TextInput
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="you@company.com"
              required
            />
          </Field>
          <Field label="Password" hint={mode === 'register' ? 'At least 8 characters.' : undefined}>
            <TextInput
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              minLength={mode === 'register' ? 8 : undefined}
              required
            />
          </Field>
          <Button type="submit" size="lg" busy={busy} style={{ width: '100%', marginTop: 6 }}>
            {mode === 'login' ? 'Sign in' : 'Create account'}
          </Button>
        </form>

        <div className={css({ textAlign: 'center', marginTop: 18 })}>
          <button
            onClick={() => {
              setMode(mode === 'login' ? 'register' : 'login');
              setError(null);
            }}
            className={css({
              background: 'none',
              border: 'none',
              cursor: 'pointer',
              fontSize: 13,
              color: T.brandText,
              fontFamily: fonts.body,
              fontWeight: 600,
            })}
          >
            {mode === 'login' ? "Don't have an account? Sign up" : 'Already have an account? Sign in'}
          </button>
        </div>
      </div>
    </div>
  );
}

export { AuthPage };
