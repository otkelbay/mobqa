import ReplayKit
import SwiftUI
import UIKit

@main
struct MobQADeviceBridgeApp: App {
    init() {
        // A locked display stops ReplayKit video delivery, so the bridge screen stays awake.
        UIApplication.shared.isIdleTimerDisabled = true
    }

    var body: some Scene {
        WindowGroup {
            ContentView()
        }
    }
}

private struct ContentView: View {
    var body: some View {
        VStack(spacing: 20) {
            Image(systemName: "iphone.radiowaves.left.and.right")
                .font(.system(size: 54))
                .foregroundStyle(.blue)
            Text("MobQA remote video")
                .font(.title2.bold())
            Text("Start the broadcast once. The screen streams to the MobQA hub over WebRTC.")
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)
                .padding(.horizontal)
            BroadcastPicker()
                .frame(maxWidth: 320)
                .frame(height: 56)
                .accessibilityLabel("Start MobQA remote video")
        }
        .padding()
    }
}

private struct BroadcastPicker: UIViewRepresentable {
    func makeUIView(context: Context) -> BroadcastLaunchButton {
        BroadcastLaunchButton()
    }

    func updateUIView(_ uiView: BroadcastLaunchButton, context: Context) {}
}

/// Wraps ReplayKit's icon-only picker in a labelled button; the system confirmation sheet is unchanged.
private final class BroadcastLaunchButton: UIView {
    private let picker = RPSystemBroadcastPickerView(frame: .zero)
    private let button = UIButton(type: .system)

    override init(frame: CGRect) {
        super.init(frame: frame)
        configure()
    }

    required init?(coder: NSCoder) {
        super.init(coder: coder)
        configure()
    }

    private func configure() {
        picker.preferredExtension = Bundle.main.bundleIdentifier.map { "\($0).broadcast" }
        picker.showsMicrophoneButton = false
        // The native picker must stay in the view hierarchy to present the system sheet.
        picker.alpha = 0.01
        picker.isUserInteractionEnabled = false
        picker.translatesAutoresizingMaskIntoConstraints = false
        addSubview(picker)
        NSLayoutConstraint.activate([
            picker.leadingAnchor.constraint(equalTo: leadingAnchor),
            picker.trailingAnchor.constraint(equalTo: trailingAnchor),
            picker.topAnchor.constraint(equalTo: topAnchor),
            picker.bottomAnchor.constraint(equalTo: bottomAnchor),
        ])

        button.setTitle("Start remote video", for: .normal)
        button.setImage(UIImage(systemName: "dot.radiowaves.left.and.right"), for: .normal)
        button.tintColor = .white
        button.titleLabel?.font = .preferredFont(forTextStyle: .headline)
        button.semanticContentAttribute = .forceLeftToRight
        button.configuration = .filled()
        button.configuration?.imagePadding = 10
        button.configuration?.cornerStyle = .large
        button.configuration?.baseBackgroundColor = .systemBlue
        button.configuration?.baseForegroundColor = .white
        button.accessibilityHint = "Opens the iOS broadcast confirmation"
        button.translatesAutoresizingMaskIntoConstraints = false
        button.addTarget(self, action: #selector(openSystemPicker), for: .touchUpInside)
        addSubview(button)
        NSLayoutConstraint.activate([
            button.leadingAnchor.constraint(equalTo: leadingAnchor),
            button.trailingAnchor.constraint(equalTo: trailingAnchor),
            button.topAnchor.constraint(equalTo: topAnchor),
            button.bottomAnchor.constraint(equalTo: bottomAnchor),
        ])
    }

    @objc private func openSystemPicker() {
        guard let nativeButton = firstButton(in: picker) else { return }
        nativeButton.sendActions(for: .touchUpInside)
    }

    private func firstButton(in view: UIView) -> UIButton? {
        if let button = view as? UIButton { return button }
        for subview in view.subviews {
            if let button = firstButton(in: subview) { return button }
        }
        return nil
    }
}
