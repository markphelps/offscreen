import CoreGraphics
import Foundation

// The Tart display hint alone does not select a macOS mode when booted headless.
// Run in the logged-in user's GUI session at each boot, and once during build.
func fail(_ message: String) -> Never {
    fputs("offscreen display: \(message)\n", stderr)
    exit(1)
}

let args = CommandLine.arguments
if args.count == 5 && args[1] == "--install" {
    let home = FileManager.default.homeDirectoryForCurrentUser
    let agents = home.appendingPathComponent("Library/LaunchAgents", isDirectory: true)
    let logs = home.appendingPathComponent("Library/Logs/offscreen", isDirectory: true)
    do {
        try FileManager.default.createDirectory(at: agents, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: logs, withIntermediateDirectories: true)
        let executable = home.appendingPathComponent("Library/Application Support/offscreen/display-selector")
        let plist: [String: Any] = [
            "Label": "com.offscreen.display",
            "ProgramArguments": [executable.path, args[2], args[3], args[4]],
            "RunAtLoad": true,
            "StandardOutPath": logs.appendingPathComponent("display.log").path,
            "StandardErrorPath": logs.appendingPathComponent("display.err").path
        ]
        let data = try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0)
        try data.write(to: agents.appendingPathComponent("com.offscreen.display.plist"), options: .atomic)
        print("offscreen display: installed per-user LaunchAgent (logs: \(logs.path))")
        exit(0)
    } catch {
        fail("install LaunchAgent: \(error)")
    }
}
if args.count != 4 || !["pt", "px"].contains(args[3]) ||
    Int(args[1]) == nil || Int(args[2]) == nil {
    fail("expected WIDTH HEIGHT pt|px")
}
let width = Int(args[1])!
let height = Int(args[2])!
if width <= 0 || height <= 0 || width > Int.max / 2 || height > Int.max / 2 {
    fail("invalid dimensions")
}
let pixelWidth = args[3] == "pt" ? width * 2 : width
let pixelHeight = args[3] == "pt" ? height * 2 : height
let options = [kCGDisplayShowDuplicateLowResolutionModes: true] as CFDictionary

func matches(_ mode: CGDisplayMode) -> Bool {
    guard mode.pixelWidth == pixelWidth && mode.pixelHeight == pixelHeight else { return false }
    return args[3] == "px" || (mode.width == width && mode.height == height)
}

// WindowServer may have started without publishing its modes yet at login.
for attempt in 0..<60 {
    let display = CGMainDisplayID()
    let modes = CGDisplayCopyAllDisplayModes(display, options) as? [CGDisplayMode] ?? []
    // With px input, prefer a 2x logical desktop when both variants exist.
    let candidate = modes.first(where: {
        matches($0) && $0.width * 2 == pixelWidth && $0.height * 2 == pixelHeight
    }) ?? modes.first(where: matches)
    if let mode = candidate {
        if let current = CGDisplayCopyDisplayMode(display), matches(current),
            current.width == mode.width && current.height == mode.height {
            print("offscreen display: already \(current.width)x\(current.height)pt / \(current.pixelWidth)x\(current.pixelHeight)px")
            exit(0)
        }
        var configuration: CGDisplayConfigRef?
        let begin = CGBeginDisplayConfiguration(&configuration)
        if begin == .success {
            let configured = CGConfigureDisplayWithDisplayMode(configuration, display, mode, nil)
            if configured == .success {
                let completed = CGCompleteDisplayConfiguration(configuration, .permanently)
                if completed == .success, let current = CGDisplayCopyDisplayMode(display),
                    matches(current), current.width == mode.width && current.height == mode.height {
                    print("offscreen display: set \(current.width)x\(current.height)pt / \(current.pixelWidth)x\(current.pixelHeight)px")
                    exit(0)
                }
                fputs("offscreen display: configuration completion failed (\(completed.rawValue))\n", stderr)
            } else {
                CGCancelDisplayConfiguration(configuration)
                fputs("offscreen display: configuration failed (\(configured.rawValue))\n", stderr)
            }
        } else {
            fputs("offscreen display: begin configuration failed (\(begin.rawValue))\n", stderr)
        }
    }
    if attempt == 59 {
        let available = modes.map { "\($0.width)x\($0.height)pt/\($0.pixelWidth)x\($0.pixelHeight)px" }
        fail("could not select \(width)x\(height)\(args[3]) (wanted \(pixelWidth)x\(pixelHeight)px) after 120s; available: \(available.joined(separator: ", "))")
    }
    Thread.sleep(forTimeInterval: 2)
}
