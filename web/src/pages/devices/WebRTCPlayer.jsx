import React from 'react';
import { deviceApi } from '../../services/apiRepository.js';
import { css } from '../../styles/styleRegistry.js';

// acquireTimeout covers ICE through a relay plus the phone's next keyframe; stallTimeout is how long
// decoded frames may stop before the viewer reconnects, which is what a manual refresh would do.
const acquireTimeout = 15000;
const stallTimeout = 6000;
const watchdogInterval = 2000;
const retryDelay = 1000;

function gatheringComplete(peer) {
  if (peer.iceGatheringState === 'complete') return Promise.resolve();
  return new Promise((resolve) => {
    peer.addEventListener('icegatheringstatechange', () => {
      if (peer.iceGatheringState === 'complete') resolve();
    });
  });
}

async function decodedFrames(peer) {
  let frames = 0;
  (await peer.getStats()).forEach((report) => {
    if (report.type === 'inbound-rtp' && report.kind === 'video') frames = report.framesDecoded ?? frames;
  });
  return frames;
}

// WebRTCPlayer watches a device's screen through the hub's SFU: one offer with every candidate, one
// answer back, as in WHEP. It reconnects on its own when frames stop arriving.
function WebRTCPlayer({ projectId, deviceId, onStatus, className, onPointer }) {
  const videoRef = React.useRef(null);
  const [attempt, setAttempt] = React.useState(0);

  React.useEffect(() => {
    let cancelled = false;
    let peer = null;
    let watchdog = null;
    let retry = null;
    const reconnect = () => {
      if (cancelled || retry) return;
      onStatus('reconnecting');
      retry = setTimeout(() => setAttempt((value) => value + 1), retryDelay);
    };

    const start = async () => {
      onStatus('connecting');
      const iceServers = await deviceApi.iceServers();
      peer = new RTCPeerConnection({ iceServers });
      peer.addTransceiver('video', { direction: 'recvonly' });
      peer.ontrack = (event) => {
        if (videoRef.current) videoRef.current.srcObject = event.streams[0] || new MediaStream([event.track]);
      };
      await peer.setLocalDescription(await peer.createOffer());
      await gatheringComplete(peer);
      const answer = await deviceApi.watch(projectId, deviceId, peer.localDescription.sdp);
      if (cancelled) return;
      await peer.setRemoteDescription({ type: 'answer', sdp: answer });

      const startedAt = Date.now();
      let lastFrames = 0;
      let progressAt = 0;
      watchdog = setInterval(async () => {
        const frames = await decodedFrames(peer);
        if (frames > lastFrames) {
          if (lastFrames === 0) onStatus('live');
          lastFrames = frames;
          progressAt = Date.now();
          return;
        }
        const stalled = progressAt ? Date.now() - progressAt > stallTimeout : Date.now() - startedAt > acquireTimeout;
        if (stalled) reconnect();
      }, watchdogInterval);
    };

    start().catch(() => !cancelled && reconnect());
    return () => {
      cancelled = true;
      clearInterval(watchdog);
      clearTimeout(retry);
      peer?.close();
    };
  }, [projectId, deviceId, attempt, onStatus]);

  return (
    <video
      ref={videoRef}
      autoPlay
      muted
      playsInline
      onPointerDown={onPointer}
      onPointerUp={onPointer}
      className={className || css({ display: 'block', maxHeight: '72vh', maxWidth: '100%' })}
    />
  );
}

export { WebRTCPlayer };
