// Package exitcode defines exit statuses reserved for runner failures.
package exitcode

const (
	// RunnerFailure means the runner failed before the wrapped command finished.
	RunnerFailure = 70
	// QueueTimeout means a job timed out before it started.
	QueueTimeout = 75
)
