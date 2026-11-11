import CoreMedia
import LiveKitWebRTC

/// Feeds ReplayKit screen buffers into a WebRTC screencast source.
final class WebRTCVideoSource {
    // 720x1560 keeps the 19.5:9 aspect of current iPhones, so browser taps map onto the full
    // point space instead of a cropped 16:9 frame.
    private static let outputWidth: Int32 = 720
    private static let outputHeight: Int32 = 1560
    // A Broadcast Upload Extension is killed above ~50 MB; the encoder's frame buffers dominate
    // that budget, and 15 fps roughly halves it compared with 30.
    private static let outputFPS: Int32 = 15

    private let factory: LKRTCPeerConnectionFactory
    private let source: LKRTCVideoSource
    private let capturer: LKRTCVideoCapturer
    let track: LKRTCVideoTrack

    init(factory: LKRTCPeerConnectionFactory) {
        self.factory = factory
        source = factory.videoSource(forScreenCast: true)
        capturer = LKRTCVideoCapturer(delegate: source)
        track = factory.videoTrack(with: source, trackId: "mobqa-screen")
        source.adaptOutputFormat(toWidth: Self.outputWidth, height: Self.outputHeight, fps: Self.outputFPS)
    }

    func push(_ sampleBuffer: CMSampleBuffer) {
        guard let pixelBuffer = CMSampleBufferGetImageBuffer(sampleBuffer) else { return }
        let timestamp = CMSampleBufferGetPresentationTimeStamp(sampleBuffer)
        let timestampNs = Int64(CMTimeGetSeconds(timestamp) * 1_000_000_000)
        let buffer = LKRTCCVPixelBuffer(pixelBuffer: pixelBuffer)
        let frame = LKRTCVideoFrame(buffer: buffer, rotation: ._0, timeStampNs: timestampNs)
        source.capturer(capturer, didCapture: frame)
    }
}
