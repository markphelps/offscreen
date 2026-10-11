// Run as an ad-hoc signed app with Input Monitoring enabled. Press a real key
// or move a real mouse, then inject input from a separately identified process.
import CoreGraphics
import Foundation

let mask = (1 << CGEventType.keyDown.rawValue) | (1 << CGEventType.mouseMoved.rawValue)
let callback: CGEventTapCallBack = { _, type, event, _ in
    if type == .tapDisabledByTimeout { return Unmanaged.passUnretained(event) }
    let pid = event.getIntegerValueField(.eventSourceUnixProcessID)
    let state = event.getIntegerValueField(.eventSourceStateID)
    print("type=\(type.rawValue) pid=\(pid) sourceState=\(state)")
    fflush(stdout)
    return Unmanaged.passUnretained(event)
}
guard let tap = CGEvent.tapCreate(tap: .cgSessionEventTap, place: .headInsertEventTap,
    options: .listenOnly, eventsOfInterest: CGEventMask(mask), callback: callback,
    userInfo: nil) else {
    fputs("Event tap unavailable; grant Input Monitoring to this executable and retry. No discrimination result.\n", stderr)
    exit(2)
}
let source = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, tap, 0)
CFRunLoopAddSource(CFRunLoopGetCurrent(), source, .commonModes)
CGEvent.tapEnable(tap: tap, enable: true)
print("Listening for physical and injected input (Ctrl-C to stop)")
CFRunLoopRun()
