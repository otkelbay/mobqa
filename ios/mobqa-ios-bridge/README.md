# MobQA iOS bridge

Streams a real iPhone's screen to the MobQA hub for remote control and session
recording. A ReplayKit Broadcast Upload extension encodes the screen once with
H.264 and publishes it over WebRTC; the hub's SFU forwards it to the browser and
records it. The host app only exists to start the broadcast.

## Build

1. Install [XcodeGen](https://github.com/yonaskolb/XcodeGen).
2. `cp Config/Local.xcconfig.example Config/Local.xcconfig` and set your bundle
   id, team, backend URL, device id and bridge token.
3. `xcodegen generate`, then build and run the `MobQADeviceBridge` scheme on the
   device.
4. Open the app and tap **Start remote video**.

Signing is automatic by default. Teams that cannot use automatic signing set
`CODE_SIGN_STYLE = Manual` and the two profile names in `Local.xcconfig`.
