import Dispatch
import os

/// Runs blocking work, such as socket round trips and launchctl, on its own
/// dispatch queue so it never holds a thread in Swift's cooperative pool. A
/// cancel can block for 140 seconds and a daemon stop for 20. The queue is
/// concurrent, and GCD adds threads while its work blocks, so parallel
/// cancels don't wait behind each other.
enum Blocking {
    private static let queue = DispatchQueue(label: "io.latentsignal.offscreen.blocking",
                                             qos: .userInitiated, attributes: .concurrent)
    // Timeouts fire from their own serial queue, which gets a thread even
    // when blocked work fills the concurrent one.
    private static let timeouts = DispatchQueue(label: "io.latentsignal.offscreen.blocking.timeouts",
                                                qos: .userInitiated)

    static func run<T: Sendable, E: Error>(_ work: @escaping @Sendable () throws(E) -> T) async throws(E) -> T {
        let result = await withCheckedContinuation { (continuation: CheckedContinuation<Result<T, E>, Never>) in
            queue.async { continuation.resume(returning: Result(catching: work)) }
        }
        return try result.get()
    }

    /// Like run, but stops waiting after `seconds` and returns nil. The work
    /// itself can't be interrupted and runs to completion on the queue.
    static func run<T: Sendable>(within seconds: Double, _ work: @escaping @Sendable () -> T) async -> T? {
        let resumed = OSAllocatedUnfairLock(initialState: false)
        return await withCheckedContinuation { (continuation: CheckedContinuation<T?, Never>) in
            let finish: @Sendable (T?) -> Void = { value in
                let first = resumed.withLock { done in
                    defer { done = true }
                    return !done
                }
                if first { continuation.resume(returning: value) }
            }
            queue.async { finish(work()) }
            timeouts.asyncAfter(deadline: .now() + seconds) { finish(nil) }
        }
    }
}
