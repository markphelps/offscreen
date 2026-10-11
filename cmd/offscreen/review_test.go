package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/project"
)

func TestBlockingRunRetainsIdentityAndWaitsForCleanupOnTruncatedStream(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "offscreen-cli-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(state) }()
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, project.FileName), []byte("project = 'hello'\n"), 0o600))
	artifacts := filepath.Join(state, "jobs", "known-id")
	cancelled := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /submit", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(daemon.Job{ID: "known-id", Artifacts: artifacts})
	})
	mux.HandleFunc("GET /jobs/known-id/stream", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "24")
		_, _ = w.Write([]byte{'o', 0, 0})
	})
	mux.HandleFunc("POST /jobs/known-id/cancel", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(250 * time.Millisecond) // model server-side clone cleanup
		close(cancelled)
		_ = json.NewEncoder(w).Encode(daemon.Job{ID: "known-id", Artifacts: artifacts, State: "cancelled"})
	})
	socket, err := daemon.Socket()
	require.NoError(t, err)
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() { require.NoError(t, server.Shutdown(context.Background())) }()
	old := os.Stderr
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = writer
	defer func() { os.Stderr = old; _ = writer.Close(); _ = reader.Close() }()
	cmd := &RunCmd{Project: "hello", Args: []string{"true"}, Worktree: root, Lane: "auto"}
	err = cmd.Run()
	require.Error(t, err)
	status, ok := errors.AsType[runExit](err)
	require.True(t, ok, "runner errors must map to exit 70")
	require.Equal(t, runExit(70), status)
	select {
	case <-cancelled:
	default:
		t.Fatal("run returned before server-side cancellation completed")
	}
	require.NoError(t, writer.Close())
	output, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Contains(t, string(output), "read job stream")
	require.True(t, strings.HasSuffix(string(output), fmt.Sprintf("job known-id artifacts %s\n", artifacts)))
}

func TestBlockingRunPreservesTaskExitCodeAndFinalLine(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "offscreen-cli-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(state) }()
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, project.FileName), []byte("project = 'hello'\n"), 0o600))
	mux := http.NewServeMux()
	job := daemon.Job{ID: "finished-id", Artifacts: filepath.Join(state, "jobs", "finished-id"), State: "done", Code: 3}
	mux.HandleFunc("POST /submit", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(job) })
	mux.HandleFunc("GET /jobs/finished-id/stream", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /jobs/finished-id", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(job) })
	socket, err := daemon.Socket()
	require.NoError(t, err)
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() { require.NoError(t, server.Shutdown(context.Background())) }()
	old := os.Stderr
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = writer
	defer func() { os.Stderr = old; _ = writer.Close(); _ = reader.Close() }()
	err = (&RunCmd{Project: "hello", Args: []string{"true"}, Worktree: root, Lane: "auto"}).Run()
	status, ok := errors.AsType[runExit](err)
	require.True(t, ok)
	require.Equal(t, runExit(3), status)
	require.NoError(t, writer.Close())
	output, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(string(output), "job finished-id artifacts "+job.Artifacts+"\n"))
}
