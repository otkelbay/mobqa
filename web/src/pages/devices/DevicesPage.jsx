import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useProject } from '../../context/ProjectContext.jsx';
import { useToast } from '../../hooks/useToasts.js';
import { API_BASE_URL } from '../../services/apiClient.js';
import { deviceApi, hubApi } from '../../services/apiRepository.js';
import { T, fonts } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import { Button, Card, EmptyState, ErrorNote, Page, SectionLabel, TextInput } from '../../components/ui/primitives.jsx';
import { IconDevices, IconPlus } from '../../components/ui/icons.jsx';

const STATUS_COLORS = {
  online: { color: T.success, background: T.successSubtle },
  unauthorized: { color: T.warning, background: T.warningSubtle },
  offline: { color: T.textMuted, background: T.bgFaint },
};

function Status({ online, label }) {
  const tone = STATUS_COLORS[online] || STATUS_COLORS.offline;
  return (
    <span
      className={css({
        fontSize: 11.5,
        fontWeight: 600,
        padding: '3px 9px',
        borderRadius: 999,
        whiteSpace: 'nowrap',
        ...tone,
      })}
    >
      {label}
    </span>
  );
}

// Secret shows a credential the server returns only once, with what to do with it.
function Secret({ title, value, children, onClose }) {
  return (
    <Card style={{ borderColor: T.brandBorder, background: T.brandTint }}>
      <div className={css({ fontSize: 14, fontWeight: 600, color: T.textPrimary, marginBottom: 6 })}>{title}</div>
      <div className={css({ fontSize: 13, color: T.textMedium, lineHeight: 1.6, marginBottom: 12 })}>{children}</div>
      <pre
        className={css({
          margin: '0 0 14px',
          padding: '12px 14px',
          background: T.bgSurface,
          border: `1px solid ${T.borderDefault}`,
          borderRadius: 9,
          fontFamily: fonts.mono,
          fontSize: 12.5,
          whiteSpace: 'pre-wrap',
          wordBreak: 'break-all',
        })}
      >
        {value}
      </pre>
      <div className={css({ display: 'flex', gap: 10 })}>
        <Button size="sm" onClick={() => navigator.clipboard?.writeText(value)}>
          Copy
        </Button>
        <Button variant="secondary" size="sm" onClick={onClose}>
          I saved it
        </Button>
      </div>
    </Card>
  );
}

function DeviceRow({ device, admin, onOpen, onStreamKey }) {
  return (
    <div
      className={css({
        display: 'flex',
        alignItems: 'center',
        gap: 14,
        padding: '13px 20px',
        borderTop: `1px solid ${T.borderLight}`,
      })}
    >
      <span className={css({ flex: 1, minWidth: 0 })}>
        <span className={css({ display: 'block', fontSize: 14, fontWeight: 600, color: T.textPrimary })}>
          {device.name}
        </span>
        <span className={css({ display: 'block', fontSize: 12, color: T.textTertiary, marginTop: 2 })}>
          {device.os} · {device.physical ? 'phone' : 'emulator'}
          {device.status !== 'online' && ` · seen ${device.lastSeen}`}
        </span>
      </span>
      <Status online={device.status} label={device.status} />
      {admin && device.platform === 'ios' && (
        <Button variant="ghost" size="sm" onClick={onStreamKey}>
          Stream key
        </Button>
      )}
      <Button variant="secondary" size="sm" onClick={onOpen} disabled={device.status !== 'online'}>
        Open
      </Button>
    </div>
  );
}

function HubCard({ hub, devices, admin, onRotate, onRemove, onStreamKey }) {
  const navigate = useNavigate();
  return (
    <Card padding={0} style={{ marginBottom: 14 }}>
      <div className={css({ display: 'flex', alignItems: 'center', gap: 12, padding: '16px 20px' })}>
        <span className={css({ flex: 1, minWidth: 0 })}>
          <span className={css({ display: 'block', fontSize: 15, fontWeight: 600, color: T.textPrimary })}>
            {hub.name}
          </span>
          <span className={css({ display: 'block', fontSize: 12, color: T.textTertiary, marginTop: 2 })}>
            {hub.version ? `hub ${hub.version} · ` : ''}seen {hub.lastSeen}
          </span>
        </span>
        <Status online={hub.online ? 'online' : 'offline'} label={hub.online ? 'connected' : 'disconnected'} />
        {admin && (
          <>
            <Button variant="ghost" size="sm" onClick={onRotate}>
              New token
            </Button>
            <Button variant="ghost" size="sm" onClick={onRemove}>
              Remove
            </Button>
          </>
        )}
      </div>
      {devices.length === 0 ? (
        <div
          className={css({
            padding: '12px 20px 16px',
            fontSize: 13,
            color: T.textTertiary,
            borderTop: `1px solid ${T.borderLight}`,
          })}
        >
          No devices attached.
        </div>
      ) : (
        devices.map((device) => (
          <DeviceRow
            key={device.id}
            device={device}
            admin={admin}
            onOpen={() => navigate(device.id)}
            onStreamKey={() => onStreamKey(device)}
          />
        ))
      )}
    </Card>
  );
}

function hubCommand(token) {
  return `MOBQA_API_URL=${API_BASE_URL} MOBQA_HUB_TOKEN=${token} mobqa-hub`;
}

function DevicesPage() {
  const { projectId, devices, reload, can } = useProject();
  const addToast = useToast();
  const [hubs, setHubs] = React.useState([]);
  const [name, setName] = React.useState('');
  const [secret, setSecret] = React.useState(null);
  const [error, setError] = React.useState(null);
  const admin = can('admin');

  const loadHubs = React.useCallback(async () => {
    try {
      setHubs(await hubApi.list(projectId));
    } catch (err) {
      setError(err);
    }
  }, [projectId]);

  React.useEffect(() => {
    loadHubs();
  }, [loadHubs]);

  // Hubs report attached devices on their own schedule; keep the list fresh while the page is open.
  React.useEffect(() => {
    const timer = setInterval(() => {
      loadHubs();
      reload('devices');
    }, 10000);
    return () => clearInterval(timer);
  }, [loadHubs, reload]);

  const act = async (request) => {
    setError(null);
    try {
      await request();
      await Promise.all([loadHubs(), reload('devices')]);
    } catch (err) {
      setError(err);
    }
  };

  const register = (e) => {
    e.preventDefault();
    act(async () => {
      const { hub, token } = await hubApi.register(projectId, name.trim());
      setName('');
      setSecret({ kind: 'hub', title: `Token for ${hub.name}`, value: hubCommand(token) });
    });
  };

  const rotate = (hub) =>
    act(async () => {
      const { token } = await hubApi.rotateToken(projectId, hub.id);
      setSecret({ kind: 'hub', title: `New token for ${hub.name}`, value: hubCommand(token) });
      addToast({ title: 'The old token no longer works', type: 'info' });
    });

  const streamKey = (device) =>
    act(async () => {
      const { key } = await deviceApi.rotateStreamKey(projectId, device.id);
      setSecret({ kind: 'stream', title: `Stream key for ${device.name}`, value: key });
    });

  return (
    <Page
      title="Devices"
      subtitle="Phones attached to your hubs. A hub is a computer that runs mobqa-hub and connects out to this server."
    >
      <ErrorNote error={error} />
      {secret && (
        <Secret title={secret.title} value={secret.value} onClose={() => setSecret(null)}>
          {secret.kind === 'hub'
            ? 'Run this on the hub computer. The token is shown once; a new one replaces it.'
            : 'Enter it in the MobQA bridge app on the phone to stream its screen. The key is shown once.'}
        </Secret>
      )}
      {hubs.length === 0 ? (
        <EmptyState
          icon={<IconDevices size={28} />}
          title="No hubs yet"
          subtitle={
            admin ? 'Register a hub, then run it on a computer with phones attached.' : 'An admin registers hubs.'
          }
        />
      ) : (
        hubs.map((hub) => (
          <HubCard
            key={hub.id}
            hub={hub}
            devices={devices.filter((device) => device.hubId === hub.id)}
            admin={admin}
            onRotate={() => rotate(hub)}
            onRemove={() => act(() => hubApi.remove(projectId, hub.id))}
            onStreamKey={streamKey}
          />
        ))
      )}
      {admin && (
        <>
          <SectionLabel>NEW HUB</SectionLabel>
          <form onSubmit={register} className={css({ display: 'flex', gap: 10, maxWidth: 520 })}>
            <TextInput
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Office Mac mini"
              required
              style={{ padding: '10px 12px' }}
            />
            <Button type="submit">
              <IconPlus size={15} viewBox="0 0 15 15" />
              Register
            </Button>
          </form>
        </>
      )}
    </Page>
  );
}

export { DevicesPage };
