import React from 'react';
import { useProject } from '../../context/ProjectContext.jsx';
import { useToast } from '../../hooks/useToasts.js';
import { buildApi } from '../../services/apiRepository.js';
import { platformLabel } from '../../services/viewModelMappers.js';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import { Button, Card, Dropzone, EmptyState, Page } from '../../components/ui/primitives.jsx';
import { IconUploads } from '../../components/ui/icons.jsx';

function platformOf(file) {
  return file.name.toLowerCase().endsWith('.ipa') ? 'ios' : 'android';
}

function Tag({ children, color, background }) {
  return (
    <span
      className={css({ fontSize: 11.5, fontWeight: 600, padding: '3px 9px', borderRadius: 999, color, background })}
    >
      {children}
    </span>
  );
}

function BuildRow({ build, editable, onCurrent, onDownload, onDelete }) {
  const expired = build.provisioning?.expired;
  return (
    <div className={css({ padding: '16px 20px', borderBottom: `1px solid ${T.borderLight}` })}>
      <div className={css({ display: 'flex', alignItems: 'center', gap: 12 })}>
        <span className={css({ width: 64, fontSize: 12, fontWeight: 600, color: T.textMuted })}>
          {platformLabel(build.platform)}
        </span>
        <span className={css({ flex: 1, minWidth: 0 })}>
          <span className={css({ display: 'block', fontFamily: fonts.mono, fontSize: 14, fontWeight: 600 })}>
            {build.label}
          </span>
          <span className={css({ display: 'block', fontSize: 12, color: T.textTertiary, marginTop: 2 })}>
            {build.fileName} · {build.size} · {build.uploaded}
          </span>
        </span>
        {build.status === 'processing' && (
          <Tag color={T.brandText} background={T.brandTint}>
            Processing
          </Tag>
        )}
        {build.isCurrent && (
          <Tag color={T.success} background={T.successSubtle}>
            Current
          </Tag>
        )}
        {build.provisioning && (
          <Tag color={expired ? T.danger : T.textMuted} background={expired ? T.dangerSubtle : T.bgFaint}>
            {expired ? 'Profile expired' : `Profile until ${build.provisioning.expires}`}
          </Tag>
        )}
        <Button variant="ghost" size="sm" onClick={onDownload}>
          Download
        </Button>
        {editable && !build.isCurrent && build.status === 'ready' && !expired && (
          <Button variant="secondary" size="sm" onClick={onCurrent}>
            Make current
          </Button>
        )}
        {editable && (
          <Button variant="ghost" size="sm" onClick={onDelete}>
            Delete
          </Button>
        )}
      </div>
      {build.changes.length > 0 && (
        <ul className={css({ margin: '10px 0 0 76px', padding: 0, listStyle: 'none' })}>
          {build.changes.map((change) => (
            <li key={change} className={css({ fontSize: 12.5, color: T.textSecondary, lineHeight: 1.6 })}>
              {change}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function BuildsPage() {
  const { projectId, project, builds, reload, can } = useProject();
  const addToast = useToast();
  const inputRef = React.useRef(null);
  const [uploading, setUploading] = React.useState(null);
  const editable = can('engineer');

  // Analysis of a fresh upload runs in the background; poll until it settles.
  React.useEffect(() => {
    if (!builds.some((build) => build.status === 'processing')) return undefined;
    const timer = setTimeout(() => reload('builds'), 3000);
    return () => clearTimeout(timer);
  }, [builds, reload]);

  const act = async (request, failure) => {
    try {
      await request();
      await reload('builds');
    } catch (err) {
      addToast({ title: failure, body: err.message, type: 'error' });
    }
  };

  const upload = async ([file]) => {
    if (!file) return;
    setUploading(file.name);
    await act(() => buildApi.upload(projectId, { file, platform: platformOf(file) }), 'Upload failed');
    setUploading(null);
    if (inputRef.current) inputRef.current.value = '';
  };

  const download = (build) =>
    act(async () => {
      const url = URL.createObjectURL(await buildApi.download(projectId, build.id));
      const link = document.createElement('a');
      link.href = url;
      link.download = build.fileName;
      link.click();
      URL.revokeObjectURL(url);
    }, 'Download failed');

  const accept = (project?.platforms || []).map((platform) => (platform === 'ios' ? '.ipa' : '.apk')).join(',');

  return (
    <Page
      title="Builds"
      subtitle="App builds that runs install on devices. Each platform runs its current build unless a run picks another."
    >
      {editable && (
        <div className={css({ marginBottom: 22 })}>
          <Dropzone
            icon={<IconUploads size={24} />}
            title={uploading ? `Uploading ${uploading}…` : 'Drop an APK or IPA here'}
            subtitle="The build is analysed for what changed since the previous one."
            action={uploading ? null : 'Choose a file'}
            onClick={uploading ? undefined : () => inputRef.current?.click()}
            inputRef={inputRef}
            accept={accept || '.apk,.ipa'}
            onFiles={upload}
          />
        </div>
      )}
      {builds.length === 0 ? (
        <EmptyState
          icon={<IconUploads size={28} />}
          title="No builds yet"
          subtitle="Upload a build to run tests on it."
        />
      ) : (
        <Card padding={0}>
          {builds.map((build) => (
            <BuildRow
              key={build.id}
              build={build}
              editable={editable}
              onCurrent={() =>
                act(() => buildApi.setCurrent(projectId, build.id), 'Could not change the current build')
              }
              onDownload={() => download(build)}
              onDelete={() => act(() => buildApi.remove(projectId, build.id), 'Could not delete the build')}
            />
          ))}
        </Card>
      )}
    </Page>
  );
}

export { BuildsPage };
