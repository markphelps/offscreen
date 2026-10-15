import Dispatch

/// Runs blocking work, such as socket round trips and launchctl, on its own
/// dispatch queue so it never holds a thread in Swift's cooperative pool. A
/// cancel can block for 140 seconds and a daemon stop for 20. The queue is
/// concurrent and overcommits, so parallel cancels each get a thread.
enum Blocking {
    private static let queue = DispatchQueue(label: "io.latentsignal.offscreen.blocking",
                                             qos: .userInitiated, attributes: .concurrent)

    static func run<T: Sendable, E: Error>(_ work: @escaping @Sendable () throws(E) -> T) async throws(E) -> T {
        let result = await withCheckedContinuation { (continuation: CheckedContinuation<Result<T, E>, Never>) in
            queue.async { continuation.resume(returning: Result(catching: work)) }
        }
        return try result.get()
    }
}
