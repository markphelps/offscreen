package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/run"
)

// phasedExecutor mimics a VM job: it reports the phases run.Execute reports
// and writes output between them, once release is closed.
func phasedExecutor(release <-chan struct{}) func(context.Context, run.Options) run.Result {
	return func(_ context.Context, o run.Options) run.Result {
		<-release
		o.Phase("preparing")
		o.Phase("cloning")
		_, _ = io.WriteString(o.Stdout, "a\n")
		o.Phase("booting")
		_, _ = io.WriteString(o.Stderr, "b\n")
		o.Phase("preparing")
		o.Phase("running")
		_, _ = io.WriteString(o.Stdout, "hi\n")
		o.Phase("collecting")
		return run.Result{}
	}
}

func readFrames(t *testing.T, r io.Reader) []byte {
	t.Helper()
	var kinds []byte
	for {
		kind, _, err := ReadFrame(r)
		if errors.Is(err, io.EOF) {
			return kinds
		}
		require.NoError(t, err)
		kinds = append(kinds, kind)
	}
}

// A client from before phase frames (such as one built from commit 6babd6e)
// requests the stream without PhasesParam and rejects any frame kind other
// than stdout and stderr. It must get exactly the job's output.
func TestStreamWithoutPhasesParamSendsOnlyOutput(t *testing.T) {
	release := make(chan struct{})
	_, client, root := fixture(t, phasedExecutor(release), func() (uint64, error) { return 20 << 30, nil })
	job := submitFixture(t, client, root, Request{Detach: true})
	waitState(t, client, job.ID, "preparing")

	// Response headers arrive with the first frame, so release the job
	// while the request is waiting.
	time.AfterFunc(100*time.Millisecond, func() { close(release) })
	raw, err := client.call(context.Background(), "GET", "/jobs/"+job.ID+"/stream", nil)
	require.NoError(t, err)
	defer func() { _ = raw.Body.Close() }()
	require.Equal(t, []byte{FrameStdout, FrameStderr, FrameStdout}, readFrames(t, raw.Body))

	var stdout, stderr bytes.Buffer
	finished, err := client.Stream(context.Background(), job.ID, &stdout, &stderr)
	require.NoError(t, err)
	require.Equal(t, "done", finished.State)
	require.Equal(t, "a\nhi\n", stdout.String())
	require.Equal(t, "b\n", stderr.String())
}

type eventLog struct{ entries []string }

func (l *eventLog) writer(prefix string) io.Writer { return eventWriter{l, prefix} }

type eventWriter struct {
	l      *eventLog
	prefix string
}

func (w eventWriter) Write(p []byte) (int, error) {
	w.l.entries = append(w.l.entries, w.prefix+string(p))
	return len(p), nil
}

func TestStreamPhasesInterleavesPhasesWithOutputInOrder(t *testing.T) {
	release := make(chan struct{})
	_, client, root := fixture(t, phasedExecutor(release), func() (uint64, error) { return 20 << 30, nil })
	job := submitFixture(t, client, root, Request{Detach: true})
	waitState(t, client, job.ID, "preparing")

	var log eventLog
	var zeroTimes int
	streamed := make(chan error, 1)
	go func() {
		_, err := client.StreamPhases(context.Background(), job.ID, log.writer("out:"), log.writer("err:"), func(p Phase) {
			if p.Time.IsZero() {
				zeroTimes++
			}
			entry := "phase:" + p.State
			if p.Reason != "" {
				entry += " (" + p.Reason + ")"
			}
			log.entries = append(log.entries, entry)
		})
		streamed <- err
	}()
	close(release)
	require.NoError(t, <-streamed)
	require.Zero(t, zeroTimes)
	// A job admitted at once still shows "queued"; terminal states are not
	// phases, and repeated states are not repeated.
	require.Equal(t, []string{
		"phase:queued",
		"phase:preparing",
		"phase:cloning",
		"out:a\n",
		"phase:booting",
		"err:b\n",
		"phase:preparing",
		"phase:running",
		"out:hi\n",
		"phase:collecting",
	}, log.entries)
}

// Phase frames are never persisted, so logs never replays them.
func TestPhasesAreNotWrittenToStreamBin(t *testing.T) {
	release := make(chan struct{})
	close(release)
	_, client, root := fixture(t, phasedExecutor(release), func() (uint64, error) { return 20 << 30, nil })
	job := submitFixture(t, client, root, Request{Detach: true})
	var phases int
	_, err := client.StreamPhases(context.Background(), job.ID, io.Discard, io.Discard, func(Phase) { phases++ })
	require.NoError(t, err)
	require.Positive(t, phases)

	f, err := os.Open(filepath.Join(job.Artifacts, "stream.bin"))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	require.Equal(t, []byte{FrameStdout, FrameStderr, FrameStdout}, readFrames(t, f))

	for _, follow := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		require.NoError(t, client.Logs(context.Background(), job.ID, follow, &stdout, &stderr))
		require.Equal(t, "a\nhi\n", stdout.String())
		require.Equal(t, "b\n", stderr.String())
	}
}

func TestQueuedPhaseCarriesAdmissionReason(t *testing.T) {
	release := make(chan struct{})
	_, client, root := fixture(t, phasedExecutor(release), func() (uint64, error) { return 1 << 30, nil }) // low memory: one VM slot
	first := submitFixture(t, client, root, Request{Detach: true})
	waitState(t, client, first.ID, "preparing")
	second := submitFixture(t, client, root, Request{Detach: true})

	var seen []Phase
	streamed := make(chan error, 1)
	go func() {
		_, err := client.StreamPhases(context.Background(), second.ID, io.Discard, io.Discard, func(p Phase) { seen = append(seen, p) })
		streamed <- err
	}()
	require.Eventually(t, func() bool {
		j, err := client.Job(context.Background(), second.ID)
		return err == nil && j.Reason != ""
	}, 5*time.Second, 20*time.Millisecond)
	close(release)
	require.NoError(t, <-streamed)
	require.NotEmpty(t, seen)
	require.Equal(t, "queued", seen[0].State)
	require.Equal(t, "waiting for VM slot", seen[0].Reason)
}

// A job finished before a daemon restart has no in-memory phases or
// stream.bin size; asking for phases must still return all its output.
func TestStreamPhasesReturnsOutputOfJobFromBeforeRestart(t *testing.T) {
	release := make(chan struct{})
	close(release)
	s, client, root := fixture(t, phasedExecutor(release), func() (uint64, error) { return 20 << 30, nil })
	job := submitFixture(t, client, root, Request{Detach: true})
	waitState(t, client, job.ID, "done")
	require.NoError(t, s.Close())

	restored, err := open(config.Default(), phasedExecutor(release), s.tart, func() (uint64, error) { return 20 << 30, nil })
	require.NoError(t, err)
	client = serveFixture(t, restored)
	var stdout, stderr bytes.Buffer
	var phases int
	finished, err := client.StreamPhases(context.Background(), job.ID, &stdout, &stderr, func(Phase) { phases++ })
	require.NoError(t, err)
	require.Equal(t, "done", finished.State)
	require.Equal(t, "a\nhi\n", stdout.String())
	require.Equal(t, "b\n", stderr.String())
	require.Zero(t, phases)
}
