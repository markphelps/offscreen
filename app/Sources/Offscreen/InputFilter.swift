// This is a candidate filter, not proof that macOS marks real hardware input
// with PID zero. The installed-app probe must compare real and injected events.
enum InputFilter {
    static func candidatePhysical(
        pid: Int64, runningJob: String?, alreadyReturning: Bool,
        manualReturn: Bool, verified: Bool
    ) -> String? {
        guard verified, !manualReturn, pid == 0, !alreadyReturning,
            let runningJob, !runningJob.isEmpty
        else { return nil }
        return runningJob
    }
}
