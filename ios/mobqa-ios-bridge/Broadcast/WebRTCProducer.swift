import Foundation
import LiveKitWebRTC

private struct BridgeConfiguration {
    let backendURL: URL
    let deviceID: String
    let token: String

    static func load() -> BridgeConfiguration? {
        guard
            let backend = Bundle.main.object(forInfoDictionaryKey: "MobQAWebRTCBackendURL") as? String,
            let deviceID = Bundle.main.object(forInfoDictionaryKey: "MobQAWebRTCDeviceID") as? String,
            let token = Bundle.main.object(forInfoDictionaryKey: "MobQAWebRTCBridgeToken") as? String,
            !backend.contains("$("), !deviceID.contains("$("), !token.contains("$("),
            let backendURL = URL(string: backend), !deviceID.isEmpty, !token.isEmpty
        else {
            return nil
        }
        return BridgeConfiguration(backendURL: backendURL, deviceID: deviceID, token: token)
    }

    var producerConfigURL: URL {
        var components = URLComponents(url: backendURL.appendingPathComponent("webrtc/producer/config"), resolvingAgainstBaseURL: false)!
        components.queryItems = [URLQueryItem(name: "device_id", value: deviceID)]
        return components.url!
    }

    var signalURL: URL {
        var components = URLComponents(url: backendURL.appendingPathComponent("webrtc/signal"), resolvingAgainstBaseURL: false)!
        components.scheme = backendURL.scheme == "https" ? "wss" : "ws"
        components.queryItems = [
            URLQueryItem(name: "role", value: "producer"),
            URLQueryItem(name: "device_id", value: deviceID),
        ]
        return components.url!
    }
}

private struct ICEConfig: Decodable {
    let urls: [String]
    let username: String?
    let credential: String?
}

private struct ProducerConfigResponse: Decodable {
    let ice_servers: [ICEConfig]
}

private struct SignalCandidate: Codable {
    let candidate: String
    let sdpMid: String?
    let sdpMLineIndex: Int32
}

private struct SignalMessage: Codable {
    let type: String
    let sdp: String?
    let candidate: SignalCandidate?

    init(type: String, sdp: String? = nil, candidate: SignalCandidate? = nil) {
        self.type = type
        self.sdp = sdp
        self.candidate = candidate
    }
}

/// WebRTC offerer inside the broadcast extension. Signalling goes to the MobQA API over
/// HTTPS/WSS; media goes to the hub's SFU over ICE or TURN.
final class WebRTCProducer: NSObject {
    // The extension's ~50 MB memory cap: an uncapped screen encoder spikes buffers on busy frames.
    private static let maxBitrateBps = 1_500_000
    private static let maxReconnectDelay: TimeInterval = 10
    private static let maxBackoffExponent = 4

    private let factory: LKRTCPeerConnectionFactory
    private let source: WebRTCVideoSource
    private let configuration: BridgeConfiguration
    private var connection: LKRTCPeerConnection?
    private var socket: URLSessionWebSocketTask?
    private var iceConfigs: [ICEConfig] = []
    private var offerStarted = false
    private var stopped = false
    private var reconnectWorkItem: DispatchWorkItem?
    private var reconnectAttempt = 0

    init?(factory: LKRTCPeerConnectionFactory, source: WebRTCVideoSource) {
        guard let configuration = BridgeConfiguration.load() else {
            NSLog("MobQA WebRTC producer is not provisioned")
            return nil
        }
        self.factory = factory
        self.source = source
        self.configuration = configuration
    }

    func start() {
        stopped = false
        reconnectAttempt = 0
        connectWithFreshConfig()
    }

    private func connectWithFreshConfig() {
        guard !stopped else { return }
        var request = URLRequest(url: configuration.producerConfigURL)
        request.setValue(configuration.token, forHTTPHeaderField: "X-MobQA-Bridge-Token")
        URLSession.shared.dataTask(with: request) { [weak self] data, response, error in
            guard let self else { return }
            guard let data, error == nil,
                  let http = response as? HTTPURLResponse, http.statusCode == 200,
                  let decoded = try? JSONDecoder().decode(ProducerConfigResponse.self, from: data) else {
                NSLog("MobQA WebRTC producer config failed")
                DispatchQueue.main.async { self.scheduleReconnect() }
                return
            }
            DispatchQueue.main.async {
                guard !self.stopped else { return }
                self.connect(iceConfigs: decoded.ice_servers)
            }
        }.resume()
    }

    func stop() {
        stopped = true
        reconnectWorkItem?.cancel()
        reconnectWorkItem = nil
        socket?.cancel(with: .goingAway, reason: nil)
        socket = nil
        connection?.close()
        connection = nil
        offerStarted = false
    }

    private func connect(iceConfigs: [ICEConfig]) {
        self.iceConfigs = iceConfigs
        guard rebuildPeerConnection() else { return }

        var request = URLRequest(url: configuration.signalURL)
        request.setValue(configuration.token, forHTTPHeaderField: "X-MobQA-Bridge-Token")
        let socket = URLSession.shared.webSocketTask(with: request)
        self.socket = socket
        socket.resume()
        receiveNextSignal(socket)
    }

    private func scheduleReconnect() {
        guard !stopped, reconnectWorkItem == nil else { return }
        let exponent = min(reconnectAttempt, Self.maxBackoffExponent)
        let delay = min(pow(2.0, Double(exponent)), Self.maxReconnectDelay)
        reconnectAttempt += 1
        let work = DispatchWorkItem { [weak self] in
            guard let self else { return }
            self.reconnectWorkItem = nil
            self.connectWithFreshConfig()
        }
        // Arm the guard before teardown: close() re-enters the ICE delegate synchronously.
        reconnectWorkItem = work
        socket?.cancel(with: .goingAway, reason: nil)
        socket = nil
        connection?.close()
        connection = nil
        offerStarted = false
        DispatchQueue.main.asyncAfter(deadline: .now() + delay, execute: work)
    }

    // An offer cannot be reused with a new viewer, so every "peer-ready" gets a fresh connection.
    private func rebuildPeerConnection() -> Bool {
        connection?.close()
        connection = nil
        offerStarted = false

        let rtcConfig = LKRTCConfiguration()
        rtcConfig.sdpSemantics = .unifiedPlan
        rtcConfig.iceServers = iceConfigs.map {
            LKRTCIceServer(urlStrings: $0.urls, username: $0.username, credential: $0.credential)
        }
        let constraints = LKRTCMediaConstraints(
            mandatoryConstraints: nil,
            optionalConstraints: [
                "OfferToReceiveAudio": "false",
                "OfferToReceiveVideo": "false",
            ]
        )
        guard let connection = factory.peerConnection(with: rtcConfig, constraints: constraints, delegate: self) else {
            NSLog("MobQA WebRTC peer connection creation failed")
            return false
        }
        let sender = connection.add(source.track, streamIds: ["mobqa-screen"])
        if let sender {
            let params = sender.parameters
            if !params.encodings.isEmpty {
                for encoding in params.encodings {
                    encoding.maxBitrateBps = NSNumber(value: Self.maxBitrateBps)
                }
                sender.parameters = params
            }
        }
        self.connection = connection
        return true
    }

    private func receiveNextSignal(_ activeSocket: URLSessionWebSocketTask) {
        activeSocket.receive { [weak self] result in
            guard let self, !self.stopped, self.socket === activeSocket else { return }
            switch result {
            case .success(let payload):
                let data: Data?
                switch payload {
                case .string(let text): data = text.data(using: .utf8)
                case .data(let bytes): data = bytes
                @unknown default: data = nil
                }
                if let data, let message = try? JSONDecoder().decode(SignalMessage.self, from: data) {
                    DispatchQueue.main.async { self.handle(message) }
                }
                self.receiveNextSignal(activeSocket)
            case .failure:
                DispatchQueue.main.async {
                    self.scheduleReconnect()
                }
            }
        }
    }

    private func handle(_ message: SignalMessage) {
        switch message.type {
        case "peer-ready":
            if offerStarted, !rebuildPeerConnection() { return }
            createOffer()
        case "answer":
            guard let sdp = message.sdp else { return }
            let answer = LKRTCSessionDescription(type: .answer, sdp: sdp)
            connection?.setRemoteDescription(answer) { error in
                if error != nil { NSLog("MobQA WebRTC answer rejected") }
            }
        case "candidate":
            guard let candidate = message.candidate else { return }
            let ice = LKRTCIceCandidate(sdp: candidate.candidate, sdpMLineIndex: candidate.sdpMLineIndex, sdpMid: candidate.sdpMid)
            connection?.add(ice) { error in
                if error != nil { NSLog("MobQA WebRTC ICE candidate rejected") }
            }
        default: break
        }
    }

    private func createOffer() {
        guard !offerStarted, let connection else { return }
        offerStarted = true
        let constraints = LKRTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
        connection.offer(for: constraints) { [weak self] offer, error in
            guard let self, let offer, error == nil else {
                self?.offerStarted = false
                return
            }
            connection.setLocalDescription(offer) { error in
                guard error == nil else {
                    self.offerStarted = false
                    return
                }
                self.send(SignalMessage(type: "offer", sdp: offer.sdp))
            }
        }
    }

    private func send(_ message: SignalMessage) {
        guard let data = try? JSONEncoder().encode(message), let text = String(data: data, encoding: .utf8) else { return }
        socket?.send(.string(text)) { error in
            if error != nil { NSLog("MobQA WebRTC signal send failed") }
        }
    }
}

extension WebRTCProducer: LKRTCPeerConnectionDelegate {
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didChange stateChanged: LKRTCSignalingState) {}
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didAdd stream: LKRTCMediaStream) {}
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didRemove stream: LKRTCMediaStream) {}
    func peerConnectionShouldNegotiate(_ peerConnection: LKRTCPeerConnection) {}
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didChange newState: LKRTCIceConnectionState) {
        guard peerConnection === connection else { return }
        if newState == .failed || newState == .closed {
            // socket and connection are confined to main; reconnecting inline would re-enter close().
            DispatchQueue.main.async { [weak self] in self?.scheduleReconnect() }
        }
    }
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didChange newState: LKRTCIceGatheringState) {}
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didGenerate candidate: LKRTCIceCandidate) {
        send(SignalMessage(type: "candidate", candidate: SignalCandidate(
            candidate: candidate.sdp,
            sdpMid: candidate.sdpMid,
            sdpMLineIndex: candidate.sdpMLineIndex
        )))
    }
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didRemove candidates: [LKRTCIceCandidate]) {}
    func peerConnection(_ peerConnection: LKRTCPeerConnection, didOpen dataChannel: LKRTCDataChannel) {}
}
