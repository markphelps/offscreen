import AVFoundation
import CoreAudio
import CoreGraphics
import Foundation
import IOKit

struct Sensors {
    static func idleSeconds() -> Double? {
        let service = IOServiceGetMatchingService(kIOMainPortDefault, IOServiceMatching("IOHIDSystem"))
        guard service != 0 else { return nil }
        defer { IOObjectRelease(service) }
        guard let value = IORegistryEntryCreateCFProperty(service, "HIDIdleTime" as CFString,
                                                         kCFAllocatorDefault, 0)?.takeRetainedValue() as? NSNumber else { return nil }
        return value.doubleValue / 1_000_000_000
    }

    static func microphoneIdle() -> Bool? {
        var address = AudioObjectPropertyAddress(mSelector: kAudioHardwarePropertyDevices,
                                                   mScope: kAudioObjectPropertyScopeGlobal,
                                                   mElement: kAudioObjectPropertyElementMain)
        var size: UInt32 = 0
        guard AudioObjectGetPropertyDataSize(AudioObjectID(kAudioObjectSystemObject), &address, 0, nil, &size) == noErr else { return nil }
        var devices = [AudioObjectID](repeating: 0, count: Int(size) / MemoryLayout<AudioObjectID>.size)
        guard AudioObjectGetPropertyData(AudioObjectID(kAudioObjectSystemObject), &address, 0, nil, &size, &devices) == noErr else { return nil }
        for device in devices {
            var input = AudioObjectPropertyAddress(mSelector: kAudioDevicePropertyStreamConfiguration,
                                                    mScope: kAudioDevicePropertyScopeInput,
                                                    mElement: kAudioObjectPropertyElementMain)
            var streamSize: UInt32 = 0
            guard AudioObjectGetPropertyDataSize(device, &input, 0, nil, &streamSize) == noErr else { return nil }
            let storage = UnsafeMutableRawPointer.allocate(byteCount: Int(streamSize), alignment: MemoryLayout<AudioBufferList>.alignment)
            defer { storage.deallocate() }
            guard AudioObjectGetPropertyData(device, &input, 0, nil, &streamSize, storage) == noErr else { return nil }
            let buffers = UnsafeMutableAudioBufferListPointer(storage.assumingMemoryBound(to: AudioBufferList.self))
            guard buffers.contains(where: { $0.mNumberChannels > 0 }) else { continue }
            var running = AudioObjectPropertyAddress(mSelector: kAudioDevicePropertyDeviceIsRunningSomewhere,
                                                      mScope: kAudioDevicePropertyScopeInput,
                                                      mElement: kAudioObjectPropertyElementMain)
            var active: UInt32 = 0
            var activeSize = UInt32(MemoryLayout<UInt32>.size)
            guard AudioObjectGetPropertyData(device, &running, 0, nil, &activeSize, &active) == noErr else { return nil }
            if active != 0 { return false }
        }
        return true
    }

    static func cameraIdle() -> Bool {
        let devices = AVCaptureDevice.DiscoverySession(deviceTypes: [.builtInWideAngleCamera, .external],
                                                       mediaType: .video, position: .unspecified).devices
        return devices.allSatisfy { !$0.isInUseByAnotherApplication }
    }

    static func snapshot(eventTapReady: Bool) -> Snapshot {
        let session = CGSessionCopyCurrentDictionary() as? [String: Any]
        // An unlocked session normally omits the lock key. Require an active,
        // logged-in console session as well; unknown values stay closed.
        let unlocked = session?["CGSSessionScreenIsLocked"] as? Bool != true
            && session?["kCGSessionOnConsoleKey"] as? Int == 1
            && session?["kCGSessionLoginDoneKey"] as? Int == 1
        return Snapshot(idleSeconds: idleSeconds() ?? 0,
                        unlocked: unlocked,
                        displayAwake: CGDisplayIsAsleep(CGMainDisplayID()) == 0,
                        cameraIdle: cameraIdle(), microphoneIdle: microphoneIdle() == true,
                        inputMonitoring: CGPreflightListenEventAccess(), eventTapReady: eventTapReady)
    }
}
