import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useAppData } from '../../context/AppDataContext.jsx';
import { buildApi, projectApi } from '../../services/apiRepository.js';
import { T } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import {
  BackLink,
  Button,
  Dropzone,
  ErrorNote,
  Field,
  IconChip,
  SegmentedTabs,
  TextArea,
  TextInput,
} from '../../components/ui/primitives.jsx';
import { IconLogo, IconUploads } from '../../components/ui/icons.jsx';

const PLATFORMS = [
  { value: 'android', label: 'Android' },
  { value: 'ios', label: 'iOS' },
  { value: 'both', label: 'Both' },
];

function platformOfBuild(file) {
  return file.name.toLowerCase().endsWith('.ipa') ? 'ios' : 'android';
}

function NewAppPage() {
  const navigate = useNavigate();
  const { reloadProjects } = useAppData();
  const [name, setName] = React.useState('');
  const [description, setDescription] = React.useState('');
  const [packageName, setPackageName] = React.useState('');
  const [platform, setPlatform] = React.useState('android');
  const [file, setFile] = React.useState(null);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState(null);
  const fileRef = React.useRef(null);

  const handleSubmit = async (e) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const platforms = platform === 'both' ? ['android', 'ios'] : [platform];
      const project = await projectApi.create({
        name: name.trim(),
        description: description.trim(),
        platforms,
        packageName: packageName.trim(),
      });
      if (file) {
        await buildApi.upload(project.id, { file, platform: platformOfBuild(file) });
      }
      await reloadProjects();
      navigate(`/apps/${project.id}/knowledge`);
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <div>
      <header
        className={css({
          display: 'flex',
          alignItems: 'center',
          gap: 16,
          padding: '20px 48px',
          borderBottom: `1px solid ${T.borderDefault}`,
          background: T.bgSurface,
          position: 'sticky',
          top: 0,
          zIndex: 5,
        })}
      >
        <div className={css({ display: 'flex', alignItems: 'center', gap: 9 })}>
          <IconChip size={26} bg={T.brand} radius={7}>
            <IconLogo size={15} />
          </IconChip>
          <span className={css({ fontSize: 18, fontWeight: 700, letterSpacing: '-0.02em', color: T.textPrimary })}>
            mobqa
          </span>
        </div>
      </header>

      <div className={css({ maxWidth: 640, margin: '0 auto', padding: '36px 48px 80px' })}>
        <BackLink onClick={() => navigate('/')}>Back to projects</BackLink>

        <h1 className={css({ fontSize: 26, fontWeight: 700, letterSpacing: '-0.02em', margin: 0, color: T.heading })}>
          New project
        </h1>
        <p className={css({ fontSize: 14, color: T.textSecondary, margin: '8px 0 26px' })}>
          Name the app and, if you have one, add a build. Tests are authored on devices of the platforms you pick.
        </p>

        <form onSubmit={handleSubmit}>
          <ErrorNote error={error} />
          <Field label="Project name">
            <TextInput value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Shop" required />
          </Field>
          <Field group label="Platforms">
            <SegmentedTabs items={PLATFORMS} value={platform} onChange={setPlatform} variant="card" />
          </Field>
          <Field label="Package name" hint="The Android package or iOS bundle id the agent launches.">
            <TextInput
              value={packageName}
              onChange={(e) => setPackageName(e.target.value)}
              placeholder="com.example.shop"
            />
          </Field>
          <Field label="Description (optional)">
            <TextArea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="What the app does, who uses it…"
              rows={3}
            />
          </Field>
          <div className={css({ marginBottom: 24 })}>
            <Dropzone
              icon={<IconUploads size={26} />}
              title={file ? file.name : 'First build (optional)'}
              subtitle={file ? `${(file.size / 1024 / 1024).toFixed(1)} MB` : 'APK or IPA'}
              action="Choose file"
              accept=".apk,.ipa"
              inputRef={fileRef}
              onClick={() => fileRef.current?.click()}
              onFiles={(files) => setFile(files[0])}
            />
          </div>
          <Button type="submit" size="lg" busy={busy} disabled={!name.trim()} style={{ width: '100%' }}>
            Create project
          </Button>
        </form>
      </div>
    </div>
  );
}

export { NewAppPage };
