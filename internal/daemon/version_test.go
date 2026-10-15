package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/run"
)

// legacyGet sends a request the way a client from before version
// negotiation (such as one built from commit 6babd6e) does: no GET /version
// probe and no ProtocolHeader.
func legacyGet(t *testing.T, c Client, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://offscreen"+path, nil)
	require.NoError(t, err)
	resp, err := c.http.Do(req)
	require.NoError(t, err)
	require.Less(t, resp.StatusCode, 400)
	return resp
}

// oldDaemon serves GET /jobs like a daemon from before version negotiation:
// it has no GET /version. It counts requests by path and records the last
// ProtocolHeader it saw.
type oldDaemon struct {
	versions, jobs atomic.Int32
	header         atomic.Value
}

func serveOldDaemon(t *testing.T) (*oldDaemon, Client) {
	t.Helper()
	state, err := os.MkdirTemp("/tmp", "osd-old-") // Unix socket paths are short on macOS.
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	listener, err := net.Listen("unix", filepath.Join(state, "offscreend.sock"))
	require.NoError(t, err)
	d := &oldDaemon{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /jobs", func(w http.ResponseWriter, r *http.Request) {
		d.jobs.Add(1)
		d.header.Store(r.Header.Get(ProtocolHeader))
		_ = json.NewEncoder(w).Encode([]Job{})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			d.versions.Add(1)
		}
		http.NotFound(w, r)
	})
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: mux}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client, err := Dial()
	require.NoError(t, err)
	return d, client
}

func TestVersionAdvertisesProtocolAndPhases(t *testing.T) {
	_, client, _ := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	info, err := client.Version(context.Background())
	require.NoError(t, err)
	require.Equal(t, ProtocolVersion, info.Protocol)
	require.Equal(t, "dev", info.Version)
	require.True(t, info.Supports(FeaturePhases))
}

func TestVersionOfOldDaemonIsProtocolZeroAndCached(t *testing.T) {
	d, client := serveOldDaemon(t)
	for range 3 {
		info, err := client.Version(context.Background())
		require.NoError(t, err)
		require.Equal(t, VersionInfo{}, info)
		require.False(t, info.Supports(FeaturePhases))
	}
	_, err := client.Jobs(context.Background())
	require.NoError(t, err)
	_, err = client.Jobs(context.Background())
	require.NoError(t, err)
	require.Equal(t, int32(1), d.versions.Load(), "one probe per client")
	require.Equal(t, int32(2), d.jobs.Load())
	require.Equal(t, strconv.Itoa(ProtocolVersion), d.header.Load())
}

func TestVersionOfStoppedDaemonIsUnavailableAndNotCached(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "osd-none-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	client, err := Dial()
	require.NoError(t, err)
	_, err = client.Version(context.Background())
	require.ErrorIs(t, err, ErrUnavailable)
	require.Nil(t, client.probe.info)
}

func TestClientRefusesDaemonBelowMinimumProtocol(t *testing.T) {
	d, client := serveOldDaemon(t)
	client.minProtocol = 1
	_, err := client.Jobs(context.Background())
	var incompatible IncompatibleError
	require.ErrorAs(t, err, &incompatible)
	require.Equal(t, IncompatibleError{Daemon: 0, Min: 1}, incompatible)
	lines := strings.Split(err.Error(), "\n")
	require.Len(t, lines, 2, "one error line plus a hint")
	require.Contains(t, lines[1], "offscreen daemon install")
	require.Zero(t, d.jobs.Load(), "no request reaches a too-old daemon")
}

func TestDaemonServesRequestsWithoutProtocolHeader(t *testing.T) {
	_, client, _ := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	resp := legacyGet(t, client, "/jobs")
	defer func() { _ = resp.Body.Close() }()
	var jobs []Job
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&jobs))
	// The menu bar app also sends no header.
	resp = legacyGet(t, client, "/host/status")
	_ = resp.Body.Close()
}
