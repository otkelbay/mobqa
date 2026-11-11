import ReplayKit
import LiveKitWebRTC

final class SampleHandler: RPBroadcastSampleHandler {
    private let factory = LKRTCPeerConnectionFactory()
    private lazy var webRTCSource = WebRTCVideoSource(factory: factory)
    private lazy var producer = WebRTCProducer(factory: factory, source: webRTCSource)
    private var receivedFirstVideoFrame = false

    override func broadcastStarted(withSetupInfo setupInfo: [String: NSObject]?) {
        NSLog("MobQA broadcast started")
        producer?.start()
    }

    override func processSampleBuffer(_ sampleBuffer: CMSampleBuffer, with sampleBufferType: RPSampleBufferType) {
        guard sampleBufferType == .video else { return }
        if !receivedFirstVideoFrame {
            receivedFirstVideoFrame = true
            NSLog("MobQA broadcast received first video frame")
        }
        webRTCSource.push(sampleBuffer)
    }

    override func broadcastFinished() {
        producer?.stop()
    }
}
