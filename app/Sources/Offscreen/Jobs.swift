import Foundation

/// A job as `GET /jobs` reports it; only the fields the app uses.
struct DaemonJob: Decodable, Sendable, Equatable {
    let id: String
    let project: String
    let task: String
    let lane: String
    let state: String
    let code: Int
    let artifacts: String
    let reason: String
    let created: Date?
    let updated: Date?

    init(
        id: String, project: String = "", task: String = "", lane: String = "vm", state: String,
        code: Int = 0, artifacts: String = "", reason: String = "", created: Date? = nil, updated: Date? = nil
    ) {
        self.id = id
        self.project = project
        self.task = task
        self.lane = lane
        self.state = state
        self.code = code
        self.artifacts = artifacts
        self.reason = reason
        self.created = created
        self.updated = updated
    }

    enum CodingKeys: String, CodingKey {
        case id, project, task, lane, state, code, artifacts, reason, created, updated
    }

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        id = try values.decode(String.self, forKey: .id)
        state = try values.decode(String.self, forKey: .state)
        project = try values.decodeIfPresent(String.self, forKey: .project) ?? ""
        task = try values.decodeIfPresent(String.self, forKey: .task) ?? ""
        lane = try values.decodeIfPresent(String.self, forKey: .lane) ?? ""
        code = try values.decodeIfPresent(Int.self, forKey: .code) ?? 0
        artifacts = try values.decodeIfPresent(String.self, forKey: .artifacts) ?? ""
        reason = try values.decodeIfPresent(String.self, forKey: .reason) ?? ""
        created = Self.date(try values.decodeIfPresent(String.self, forKey: .created))
        updated = Self.date(try values.decodeIfPresent(String.self, forKey: .updated))
    }

    /// Go writes RFC 3339 with optional fractional seconds. ISO8601DateFormatter is thread-safe
    /// but not Sendable, so one pair serves every decode instead of allocating per job on each refresh.
    nonisolated(unsafe) private static let fractionalFormatter: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()
    nonisolated(unsafe) private static let plainFormatter = ISO8601DateFormatter()

    static func date(_ text: String?) -> Date? {
        guard let text else { return nil }
        return fractionalFormatter.date(from: text) ?? plainFormatter.date(from: text)
    }

    /// Queued or in flight: cancelling it stops a worker or a queued entry.
    var active: Bool { !["done", "failed", "cancelled"].contains(state) }

    /// "hello · launch", or the short job ID for a command with no task.
    var name: String { "\(project) · \(task.isEmpty ? String(id.prefix(8)) : task)" }

    /// The result of a finished job: "done", "failed (exit 1)", "cancelled".
    var result: String { state == "failed" && code != 0 ? "failed (exit \(code))" : state }
}

/// The menu's job sections: queued and running jobs oldest first, then the
/// most recently finished ones.
struct JobList: Equatable {
    static let recentLimit = 5
    var active: [DaemonJob] = []
    var recent: [DaemonJob] = []

    init(active: [DaemonJob] = [], recent: [DaemonJob] = []) {
        self.active = active
        self.recent = recent
    }

    init(_ jobs: [DaemonJob]) {
        active = jobs.filter(\.active).sorted { ($0.created ?? .distantPast) < ($1.created ?? .distantPast) }
        recent = Array(
            jobs.filter { !$0.active }
                .sorted { ($0.updated ?? .distantPast) > ($1.updated ?? .distantPast) }
                .prefix(Self.recentLimit))
    }

    /// "hello · launch — vm · running · 1:05"
    static func activeTitle(_ job: DaemonJob, now: Date) -> String {
        "\(job.name) — \(job.lane) · \(job.state) · \(elapsed(since: job.created, now: now))"
    }

    /// "hello · launch — vm · failed (exit 1) · 3m ago"
    static func recentTitle(_ job: DaemonJob, now: Date) -> String {
        "\(job.name) — \(job.lane) · \(job.result) · \(ago(job.updated, now: now))"
    }

    /// "0:42", "12:05", "1:02:03".
    static func elapsed(since start: Date?, now: Date) -> String {
        let total = max(0, Int(now.timeIntervalSince(start ?? now)))
        let (hours, minutes, seconds) = (total / 3600, total / 60 % 60, total % 60)
        return hours > 0
            ? String(format: "%d:%02d:%02d", hours, minutes, seconds)
            : String(format: "%d:%02d", minutes, seconds)
    }

    static func ago(_ date: Date?, now: Date) -> String {
        let seconds = max(0, Int(now.timeIntervalSince(date ?? now)))
        switch seconds {
        case ..<60: return "just now"
        case ..<3600: return "\(seconds / 60)m ago"
        case ..<86400: return "\(seconds / 3600)h ago"
        default: return "\(seconds / 86400)d ago"
        }
    }
}

/// Finds jobs that failed since the app first saw the job list, so each
/// failure notifies once and failures from before launch don't.
struct FailureTracker {
    private var seen: Set<String>?

    /// Returns the jobs that newly reached `failed`.
    mutating func newFailures(in jobs: [DaemonJob]) -> [DaemonJob] {
        let failed = jobs.filter { $0.state == "failed" }
        defer { seen = (seen ?? []).union(failed.map(\.id)) }
        guard let seen else { return [] }
        return failed.filter { !seen.contains($0.id) }
    }
}

/// The menu bar icon's aggregate state.
enum IconState: Equatable {
    case idle, running, attention

    /// The status item's accessibility label, so scripts can read the state.
    var label: String {
        switch self {
        case .idle: "Offscreen"
        case .running: "Offscreen, jobs running"
        case .attention: "Offscreen, needs attention"
        }
    }

    /// Attention wins: an unacknowledged failure, a pending host action
    /// (countdown, Not now, Take back) or a failed control.
    static func of(jobs: [DaemonJob], host: HostStatus?, unseenFailures: Bool, actionError: Bool) -> IconState {
        let hostPending = host.map { $0.countdownJob != nil || $0.notNowJob != nil || $0.returnJob != nil } ?? false
        if unseenFailures || hostPending || actionError { return .attention }
        if jobs.contains(where: \.active) || host?.running != nil { return .running }
        return .idle
    }
}
