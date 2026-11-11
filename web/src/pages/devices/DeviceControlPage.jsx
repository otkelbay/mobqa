import React from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { useProject } from '../../context/ProjectContext.jsx';
import { useToast } from '../../hooks/useToasts.js';
import { deviceApi } from '../../services/apiRepository.js';
import { T } from '../../styles/tokens.js';
import { css } from '../../styles/styleRegistry.js';
import {
  BackLink,
  Button,
  Card,
  ErrorNote,
  Field,
  Page,
  SegmentedTabs,
  Spinner,
  TextInput,
} from '../../components/ui/primitives.jsx';
import { PlatformBadge } from '../../components/ui/Badges.jsx';
import { WebRTCPlayer } from './WebRTCPlayer.jsx';

// tapSlop is how far a pointer may travel and still count as a tap; longPress is when a held tap
// becomes a long press, as on the phone itself.
const tapSlop = 12;
const longPress = 500;
// screenshotInterval keeps a polled screen usable without saturating the hub link.
const screenshotInterval = 900;

const MODES = [
  { value: 'video', label: 'Live video' },
  { value: 'screenshots', label: 'Screenshots' },
];

const screenStyle = css({
  display: 'block',
  maxHeight: '68vh',
  maxWidth: '100%',
  borderRadius: 18,
  background: '#000',
  cursor: 'crosshair',
  touchAction: 'none',
  userSelect: 'none',
});

// devicePoint maps a pointer on the shown screen to device pixels.
function devicePoint(event, size) {
  const box = event.currentTarget.getBoundingClientRect();
  return {
    x: Math.round(((event.clientX - box.left) / box.width) * size.width),
    y: Math.round(((event.clientY - box.top) / box.height) * size.height),
    screenX: event.clientX,
    screenY: event.clientY,
  };
}

function gestureCommand(start, end, heldFor) {
  const moved = Math.hypot(end.screenX - start.screenX, end.screenY - start.screenY);
  if (moved > tapSlop) {
    return { kind: 'swipe', x: start.x, y: start.y, to_x: end.x, to_y: end.y, duration_ms: Math.max(heldFor, 150) };
  }
  if (heldFor >= longPress) return { kind: 'long_press', x: start.x, y: start.y, duration_ms: heldFor };
  return { kind: 'tap', x: start.x, y: start.y };
}

function useScreenshot(projectId, deviceId, polling) {
  const [screen, setScreen] = React.useState(null);
  const [tick, setTick] = React.useState(0);
  const refresh = React.useCallback(() => setTick((value) => value + 1), []);

  React.useEffect(() => {
    let live = true;
    let url = null;
    deviceApi
      .screen(projectId, deviceId)
      .then((blob) => {
        url = URL.createObjectURL(blob);
        const image = new Image();
        image.onload = () => {
          if (!live) return;
          setScreen((prev) => {
            if (prev) URL.revokeObjectURL(prev.url);
            return { url, width: image.naturalWidth, height: image.naturalHeight };
          });
        };
        image.src = url;
      })
      .catch(() => {});
    const timer = polling ? setTimeout(refresh, screenshotInterval) : null;
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [projectId, deviceId, polling, tick, refresh]);

  return { screen, refresh };
}

function Controls({ packageName, send }) {
  const [text, setText] = React.useState('');
  const [url, setUrl] = React.useState('');
  return (
    <Card>
      <Field group label="Type">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            send({ kind: 'type', text });
            setText('');
          }}
          className={css({ display: 'flex', gap: 8 })}
        >
          <TextInput value={text} onChange={(e) => setText(e.target.value)} style={{ padding: '9px 11px' }} />
          <Button type="submit" variant="secondary" size="sm" disabled={!text}>
            Send
          </Button>
        </form>
      </Field>
      <div className={css({ display: 'flex', flexWrap: 'wrap', gap: 8, marginBottom: 18 })}>
        <Button variant="secondary" size="sm" onClick={() => send({ kind: 'back' })}>
          Back
        </Button>
        <Button variant="secondary" size="sm" onClick={() => send({ kind: 'enter' })}>
          Enter
        </Button>
        <Button variant="secondary" size="sm" onClick={() => send({ kind: 'clear' })}>
          Clear field
        </Button>
        <Button variant="secondary" size="sm" onClick={() => send({ kind: 'hide_keyboard' })}>
          Hide keyboard
        </Button>
      </div>
      {packageName && (
        <div className={css({ display: 'flex', gap: 8, marginBottom: 18 })}>
          <Button variant="secondary" size="sm" onClick={() => send({ kind: 'launch', text: packageName })}>
            Launch app
          </Button>
          <Button variant="secondary" size="sm" onClick={() => send({ kind: 'stop', text: packageName })}>
            Stop app
          </Button>
        </div>
      )}
      <Field group label="Open a link">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            send({ kind: 'open_url', text: url });
          }}
          className={css({ display: 'flex', gap: 8 })}
        >
          <TextInput
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="myapp://orders/42"
            style={{ padding: '9px 11px' }}
          />
          <Button type="submit" variant="secondary" size="sm" disabled={!url}>
            Open
          </Button>
        </form>
      </Field>
    </Card>
  );
}

function DeviceControlPage() {
  const navigate = useNavigate();
  const { deviceId } = useParams();
  const { projectId, project, can } = useProject();
  const addToast = useToast();
  const [device, setDevice] = React.useState(null);
  const [mode, setMode] = React.useState(null);
  const [status, setStatus] = React.useState('');
  const [error, setError] = React.useState(null);
  const gesture = React.useRef(null);
  const editable = can('engineer');
  const { screen, refresh } = useScreenshot(projectId, deviceId, mode === 'screenshots');

  React.useEffect(() => {
    deviceApi
      .get(projectId, deviceId)
      .then((loaded) => {
        setDevice(loaded);
        setMode(loaded.platform === 'ios' ? 'video' : 'screenshots');
      })
      .catch(setError);
  }, [projectId, deviceId]);

  const send = async (command) => {
    try {
      await deviceApi.command(projectId, deviceId, command);
      if (mode === 'screenshots') refresh();
    } catch (err) {
      addToast({ title: 'The device did not respond', body: err.message, type: 'error' });
    }
  };

  const onPointer = (event) => {
    if (!editable || !screen) return;
    const point = devicePoint(event, screen);
    if (event.type === 'pointerdown') {
      gesture.current = { point, at: Date.now() };
      return;
    }
    if (!gesture.current) return;
    send(gestureCommand(gesture.current.point, point, Date.now() - gesture.current.at));
    gesture.current = null;
  };

  const back = <BackLink onClick={() => navigate(`/apps/${projectId}/devices`)}>Back to devices</BackLink>;
  if (!device) {
    return (
      <Page title="Device" back={back}>
        {error ? <ErrorNote error={error} /> : <Spinner />}
      </Page>
    );
  }

  return (
    <Page title={device.name} subtitle={`${device.os} · ${device.hubName}`} back={back} width={1080}>
      <div className={css({ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 18 })}>
        <PlatformBadge platform={device.platform} />
        <SegmentedTabs items={MODES} value={mode} onChange={setMode} />
        <span className={css({ fontSize: 12.5, color: T.textTertiary })}>
          {mode === 'video' ? status : 'Refreshes about once a second'}
        </span>
      </div>
      <div
        className={css({
          display: 'grid',
          gridTemplateColumns: editable ? 'auto 1fr' : 'auto',
          alignItems: 'start',
          gap: 24,
        })}
      >
        <div>
          {mode === 'video' ? (
            <WebRTCPlayer
              projectId={projectId}
              deviceId={deviceId}
              onStatus={setStatus}
              onPointer={onPointer}
              className={screenStyle}
            />
          ) : screen ? (
            <img
              src={screen.url}
              alt={`${device.name} screen`}
              draggable={false}
              onPointerDown={onPointer}
              onPointerUp={onPointer}
              className={screenStyle}
            />
          ) : (
            <Spinner />
          )}
          {editable && (
            <p className={css({ fontSize: 12.5, color: T.textTertiary, marginTop: 10 })}>
              Click to tap, hold to long-press, drag to swipe.
            </p>
          )}
        </div>
        {editable && <Controls packageName={project?.packageName} send={send} />}
      </div>
    </Page>
  );
}

export { DeviceControlPage };
