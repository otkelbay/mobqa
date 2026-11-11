import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppData } from '../../context/AppDataContext.jsx';
import { useToast } from '../../hooks/useToasts.js';
import {
  Avatar,
  BackLink,
  Button,
  Card,
  ErrorNote,
  Field,
  Page,
  Select,
  TextInput,
} from '../../components/ui/primitives.jsx';

const LANGUAGES = [
  { value: 'en', label: 'English' },
  { value: 'ru', label: 'Русский' },
];

function initialsOf(name) {
  return (
    (name || '')
      .split(/\s+/)
      .filter(Boolean)
      .map((part) => part[0])
      .join('')
      .slice(0, 2)
      .toUpperCase() || 'U'
  );
}

function ProfilePage() {
  const navigate = useNavigate();
  const addToast = useToast();
  const { user, updateProfile, logout } = useAppData();
  const [form, setForm] = React.useState({ name: '', timezone: '', language: 'en' });
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);

  React.useEffect(() => {
    if (user) setForm({ name: user.name, timezone: user.timezone, language: user.language });
  }, [user]);

  const set = (key) => (e) => setForm((prev) => ({ ...prev, [key]: e.target.value }));

  const save = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await updateProfile({ name: form.name.trim(), timezone: form.timezone.trim(), language: form.language });
      addToast({ title: 'Profile saved', type: 'success' });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Page
      title="Profile"
      subtitle={user?.email}
      width={640}
      back={<BackLink onClick={() => navigate('/')}>Back to projects</BackLink>}
      actions={<Avatar initials={initialsOf(user?.name)} size={46} />}
    >
      <Card>
        <form onSubmit={save}>
          <ErrorNote error={error} />
          <Field label="Name">
            <TextInput value={form.name} onChange={set('name')} required />
          </Field>
          <Field label="Time zone" hint="An IANA name, such as Europe/Berlin.">
            <TextInput value={form.timezone} onChange={set('timezone')} />
          </Field>
          <Field label="Language">
            <Select options={LANGUAGES} value={form.language} onChange={set('language')} />
          </Field>
          <Button type="submit" busy={busy}>
            Save
          </Button>
        </form>
      </Card>
      <Button variant="dangerOutline" onClick={logout}>
        Sign out
      </Button>
    </Page>
  );
}

export { ProfilePage };
