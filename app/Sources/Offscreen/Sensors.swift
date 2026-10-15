import CoreMediaIO
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

    static func cameraIdle(deviceStates: [UInt32?]) -> Bool? {
        guard deviceStates.allSatisfy({ $0 != nil }) else { return nil }
        return deviceStates.allSatisfy { $0 == 0 }
    }

    static func cameraIdle() -> Bool? {
        var address = CMIOObjectPropertyAddress(
            mSelector: CMIOObjectPropertySelector(kCMIOHardwarePropertyDevices),
            mScope: CMIOObjectPropertyScope(kCMIOObjectPropertyScopeGlobal),
            mElement: CMIOObjectPropertyElement(kCMIOObjectPropertyElementMain))
        let system = CMIOObjectID(kCMIOObjectSystemObject)
        let stride = MemoryLayout<CMIOObjectID>.size
        var size: UInt32 = 0
        guard CMIOObjectGetPropertyDataSize(system, &address, 0, nil, &size) == noErr,
              Int(size) % stride == 0 else { return nil }
        if size == 0 { return cameraIdle(deviceStates: []) }
        var devices = [CMIOObjectID](repeating: 0, count: Int(size) / stride)
        var used: UInt32 = 0
        guard CMIOObjectGetPropertyData(system, &address, 0, nil, size, &used, &devices) == noErr,
              used <= size, Int(used) % stride == 0 else { return nil }
        var states: [UInt32?] = []
        for device in devices.prefix(Int(used) / stride) {
            var running = CMIOObjectPropertyAddress(
                mSelector: CMIOObjectPropertySelector(kCMIODevicePropertyDeviceIsRunningSomewhere),
                mScope: CMIOObjectPropertyScope(kCMIOObjectPropertyScopeGlobal),
                mElement: CMIOObjectPropertyElement(kCMIOObjectPropertyElementMain))
            var active: UInt32 = 0
            var returned: UInt32 = 0
            let expected = UInt32(MemoryLayout<UInt32>.size)
            let status = CMIOObjectGetPropertyData(device, &running, 0, nil, expected, &returned, &active)
            states.append(status == noErr && returned == expected ? active : nil)
        }
        return cameraIdle(deviceStates: states)
    }

    static func sessionUnlocked(_ session: [String: Any]?) -> Bool {
        session?["CGSSessionScreenIsLocked"] as? Bool != true
            && session?[kCGSessionOnConsoleKey as String] as? Int == 1
            && session?[kCGSessionLoginDoneKey as String] as? Int == 1
    }

    static func snapshot(eventTapReady: Bool) -> Snapshot {
        let session = CGSessionCopyCurrentDictionary() as? [String: Any]
        // An unlocked session normally omits the lock key. Require an active,
        // logged-in console session as well; unknown values stay closed.
        let unlocked = sessionUnlocked(session)
        return Snapshot(idleSeconds: idleSeconds() ?? 0,
                        unlocked: unlocked,
                        displayAwake: CGDisplayIsAsleep(CGMainDisplayID()) == 0,
                        cameraIdle: cameraIdle() == true, microphoneIdle: microphoneIdle() == true,
                        inputMonitoring: CGPreflightListenEventAccess(), eventTapReady: eventTapReady)
    }
}
