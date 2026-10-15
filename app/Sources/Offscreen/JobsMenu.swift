import SwiftUI

/// Queued and running jobs from both lanes, then the last finished ones,
/// each with a submenu of its actions.
struct JobsMenu: View {
    let model: HostModel

    var body: some View {
        Text("Jobs")
        if model.jobs.active.isEmpty { Text("No queued or running jobs") }
        ForEach(model.jobs.active, id: \.id) { job in
            Menu(JobList.activeTitle(job, now: model.now)) {
                if !job.reason.isEmpty { Text(job.reason) }
                Button(model.cancelling.contains(job.id) ? "Cancelling…" : "Cancel") { model.cancel(job) }
                    .disabled(model.cancelling.contains(job.id))
                Button("Copy Job ID") { model.copyID(job.id) }
            }
        }
        if !model.jobs.recent.isEmpty {
            Divider()
            Text("Recently finished")
            ForEach(model.jobs.recent, id: \.id) { job in
                Menu(JobList.recentTitle(job, now: model.now)) {
                    if !job.reason.isEmpty { Text(job.reason) }
                    Button("Reveal Artifacts") { model.revealArtifacts(job.artifacts) }
                        .disabled(job.artifacts.isEmpty)
                    Button("Copy Job ID") { model.copyID(job.id) }
                }
            }
        }
    }
}
