import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppData } from '../../context/AppDataContext.jsx';
import { useProject } from '../../context/ProjectContext.jsx';
import { useToast } from '../../hooks/useToasts.js';
import { projectApi } from '../../services/apiRepository.js';
import { T } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import {
  Button,
  Card,
  ErrorNote,
  Field,
  Page,
  SectionLabel,
  SegmentedTabs,
  Select,
  TextArea,
  TextInput,
} from '../../components/ui/primitives.jsx';

const PLATFORMS = [
  { value: 'android', label: 'Android' },
  { value: 'ios', label: 'iOS' },
  { value: 'both', label: 'Both' },
];

const METHODS = [
  { value: 'none', label: 'No sign-in' },
  { value: 'password', label: 'Login and password' },
  { value: 'phone_otp', label: 'Code sent to a phone' },
  { value: 'email_otp', label: 'Code sent to an email' },
  { value: 'google_sso', label: 'Google account' },
  { value: 'apple_sso', label: 'Apple account' },
];

const ROLES = [
  { value: 'viewer', label: 'Viewer' },
  { value: 'engineer', label: 'Engineer' },
  { value: 'admin', label: 'Admin' },
];

const ROLE_LABELS = { owner: 'Owner', admin: 'Admin', engineer: 'Engineer', viewer: 'Viewer' };

// useSave runs one form's request, reporting success as a toast and keeping the failure for the form.
function useSave(successTitle) {
  const addToast = useToast();
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);
  const run = async (request) => {
    setBusy(true);
    setError(null);
    try {
      await request();
      if (successTitle) addToast({ title: successTitle, type: 'success' });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return { busy, error, run };
}

function GeneralSection({ project, editable, onSaved }) {
  const [form, setForm] = React.useState(() => ({
    name: project.name,
    description: project.description,
    packageName: project.packageName,
    platform: project.platforms.length > 1 ? 'both' : project.platforms[0] || 'android',
  }));
  const save = useSave('Project saved');
  const set = (key) => (e) => setForm((prev) => ({ ...prev, [key]: e.target.value }));

  const submit = (e) => {
    e.preventDefault();
    save.run(async () => {
      await projectApi.update(project.id, {
        name: form.name.trim(),
        description: form.description.trim(),
        packageName: form.packageName.trim(),
        platforms: form.platform === 'both' ? ['android', 'ios'] : [form.platform],
      });
      await onSaved();
    });
  };

  return (
    <Card>
      <form onSubmit={submit}>
        <ErrorNote error={save.error} />
        <Field label="Name">
          <TextInput value={form.name} onChange={set('name')} disabled={!editable} required />
        </Field>
        <Field group label="Platforms">
          <SegmentedTabs
            items={PLATFORMS}
            value={form.platform}
            onChange={(platform) => editable && setForm((prev) => ({ ...prev, platform }))}
            variant="card"
          />
        </Field>
        <Field label="Package name" hint="The Android package or iOS bundle id the agent launches.">
          <TextInput value={form.packageName} onChange={set('packageName')} disabled={!editable} />
        </Field>
        <Field label="Description" hint="The agent reads it when it generates tests.">
          <TextArea value={form.description} onChange={set('description')} rows={3} disabled={!editable} />
        </Field>
        {editable && (
          <Button type="submit" busy={save.busy}>
            Save
          </Button>
        )}
      </form>
    </Card>
  );
}

function variableOptions(environments) {
  const names = [...new Set(environments.flatMap((env) => env.variables.map((variable) => variable.name)))];
  return [{ value: '', label: 'Choose a variable' }, ...names.map((name) => ({ value: name, label: `{{${name}}}` }))];
}

function SignInSection({ project, environments, actions, editable, onSaved }) {
  const [profile, setProfile] = React.useState(project.authProfile);
  const save = useSave('Sign-in saved');
  const set = (key) => (e) => setProfile((prev) => ({ ...prev, [key]: e.target.value }));
  const variables = variableOptions(environments);
  const actionOptions = [
    { value: '', label: 'None' },
    ...actions.map((action) => ({ value: action.id, label: action.name })),
  ];
  const method = profile.method;
  const otp = method === 'phone_otp' || method === 'email_otp';
  const sso = method.endsWith('_sso');

  const submit = (e) => {
    e.preventDefault();
    save.run(async () => {
      await projectApi.setAuthProfile(project.id, profile);
      await onSaved();
    });
  };

  return (
    <Card>
      <p className={css({ fontSize: 13, color: T.textSecondary, margin: '0 0 18px', lineHeight: 1.6 })}>
        The authoring agent follows this to sign in. Values come from the run&apos;s environment and never reach the
        language model; it only sees the variable names.
      </p>
      <form onSubmit={submit}>
        <ErrorNote error={save.error} />
        <Field label="How people sign in">
          <Select options={METHODS} value={method} onChange={set('method')} disabled={!editable} />
        </Field>
        {(method === 'password' || otp) && (
          <Field label={otp ? 'Phone or email variable' : 'Login variable'}>
            <Select options={variables} value={profile.loginVar} onChange={set('loginVar')} disabled={!editable} />
          </Field>
        )}
        {method === 'password' && (
          <Field label="Password variable">
            <Select
              options={variables}
              value={profile.passwordVar}
              onChange={set('passwordVar')}
              disabled={!editable}
            />
          </Field>
        )}
        {otp && (
          <>
            <Field label="Fixed test code" hint="Leave empty when test builds send real codes.">
              <TextInput value={profile.otpOverride} onChange={set('otpOverride')} disabled={!editable} />
            </Field>
            <Field label="API action that reads the code" hint="The agent uses the variables its extractions fill.">
              <Select
                options={actionOptions}
                value={profile.otpActionId}
                onChange={set('otpActionId')}
                disabled={!editable}
              />
            </Field>
          </>
        )}
        {sso && (
          <Field label="Account variable">
            <Select
              options={variables}
              value={profile.ssoAccountVar}
              onChange={set('ssoAccountVar')}
              disabled={!editable}
            />
          </Field>
        )}
        <Field label="Notes for the agent">
          <TextArea
            value={profile.notes}
            onChange={set('notes')}
            rows={2}
            placeholder="e.g. Skip the onboarding carousel first."
            disabled={!editable}
          />
        </Field>
        {editable && (
          <Button type="submit" busy={save.busy}>
            Save
          </Button>
        )}
      </form>
    </Card>
  );
}

function MembersSection({ project, editable, onSaved }) {
  const [email, setEmail] = React.useState('');
  const [role, setRole] = React.useState('engineer');
  const save = useSave(null);

  const add = (e) => {
    e.preventDefault();
    save.run(async () => {
      await projectApi.addMember(project.id, { email: email.trim(), role });
      setEmail('');
      await onSaved();
    });
  };
  const change = (userId, next) =>
    save.run(async () => {
      await projectApi.setMemberRole(project.id, userId, next);
      await onSaved();
    });
  const remove = (userId) =>
    save.run(async () => {
      await projectApi.removeMember(project.id, userId);
      await onSaved();
    });

  return (
    <Card padding={0}>
      <div className={css({ padding: '18px 22px 4px' })}>
        <ErrorNote error={save.error} />
      </div>
      {project.members.map((member) => (
        <div
          key={member.userId}
          className={css({
            display: 'flex',
            alignItems: 'center',
            gap: 14,
            padding: '12px 22px',
            borderBottom: `1px solid ${T.borderDivider}`,
          })}
        >
          <div className={css({ flex: 1, minWidth: 0 })}>
            <div className={css({ fontSize: 13.5, fontWeight: 600, color: T.textPrimary })}>{member.name}</div>
            <div className={css({ fontSize: 12, color: T.textTertiary })}>{member.email}</div>
          </div>
          {editable && member.role !== 'owner' ? (
            <>
              <Select
                options={ROLES}
                value={member.role}
                onChange={(e) => change(member.userId, e.target.value)}
                style={{ width: 140, padding: '8px 30px 8px 12px', fontSize: 13 }}
              />
              <Button variant="ghost" size="sm" onClick={() => remove(member.userId)}>
                Remove
              </Button>
            </>
          ) : (
            <span className={css({ fontSize: 12.5, color: T.textMuted })}>{ROLE_LABELS[member.role]}</span>
          )}
        </div>
      ))}
      {editable && (
        <form onSubmit={add} className={css({ display: 'flex', gap: 10, padding: '16px 22px' })}>
          <TextInput
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="colleague@company.com"
            required
            style={{ padding: '10px 12px' }}
          />
          <Select
            options={ROLES}
            value={role}
            onChange={(e) => setRole(e.target.value)}
            style={{ width: 150, padding: '10px 30px 10px 12px' }}
          />
          <Button type="submit" busy={save.busy}>
            Add
          </Button>
        </form>
      )}
    </Card>
  );
}

function ArchiveSection({ project }) {
  const navigate = useNavigate();
  const { reloadProjects } = useAppData();
  const [confirming, setConfirming] = React.useState(false);
  const save = useSave('Project archived');

  const archive = () =>
    save.run(async () => {
      await projectApi.archive(project.id);
      await reloadProjects();
      navigate('/');
    });

  return (
    <Card>
      <ErrorNote error={save.error} />
      <p className={css({ fontSize: 13, color: T.textSecondary, margin: '0 0 14px' })}>
        Archiving hides the project from every member. Its tests, runs and builds are kept.
      </p>
      {confirming ? (
        <div className={css({ display: 'flex', gap: 10 })}>
          <Button variant="danger" busy={save.busy} onClick={archive}>
            Archive {project.name}
          </Button>
          <Button variant="secondary" onClick={() => setConfirming(false)}>
            Cancel
          </Button>
        </div>
      ) : (
        <Button variant="dangerOutline" onClick={() => setConfirming(true)}>
          Archive project
        </Button>
      )}
    </Card>
  );
}

function ProjectSettingsPage() {
  const { project, environments, actions, reload, can } = useProject();
  const { reloadProjects } = useAppData();

  if (!project) return null;

  const onProjectSaved = async () => {
    await Promise.all([reload('project'), reloadProjects()]);
  };

  return (
    <Page title="Settings" subtitle="The project, how its app signs in, and who works on it" width={760}>
      <SectionLabel>GENERAL</SectionLabel>
      <GeneralSection key={project.id} project={project} editable={can('admin')} onSaved={onProjectSaved} />
      <SectionLabel>SIGN-IN</SectionLabel>
      <SignInSection
        project={project}
        environments={environments}
        actions={actions}
        editable={can('engineer')}
        onSaved={() => reload('project')}
      />
      <SectionLabel>MEMBERS</SectionLabel>
      <MembersSection project={project} editable={can('admin')} onSaved={() => reload('project')} />
      {can('owner') && (
        <>
          <SectionLabel>ARCHIVE</SectionLabel>
          <ArchiveSection project={project} />
        </>
      )}
    </Page>
  );
}

export { ProjectSettingsPage };
