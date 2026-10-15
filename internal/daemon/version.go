package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/markphelps/offscreen/internal/version"
)

// ProtocolVersion is the wire protocol this build speaks. Bump it whenever
// the socket API gains or changes behavior. A daemon from before version
// negotiation has no GET /version and counts as protocol 0.
const ProtocolVersion = 1

// MinDaemonProtocol is the oldest daemon protocol this client works with.
// Commands fail against an older daemon with a hint to restart it.
const MinDaemonProtocol = 0

// ProtocolHeader carries the client's ProtocolVersion on every request, so
// the daemon can log it and, if it ever must, refuse a client with a 4xx.
// Clients from before negotiation, and the menu bar app, do not send it.
const ProtocolHeader = "X-Offscreen-Protocol"

// FeaturePhases means GET /jobs/{id}/stream honors PhasesParam.
const FeaturePhases = "phases"

// features are the opt-in capabilities this daemon advertises.
var features = []string{FeaturePhases}

// VersionInfo is the GET /version response.
type VersionInfo struct {
	// Protocol is the daemon's ProtocolVersion; 0 for a daemon without
	// GET /version.
	Protocol int `json:"protocol"`
	// Version is the daemon's build version, for diagnostics only.
	Version string `json:"version"`
	// Features names the opt-in capabilities the daemon supports.
	Features []string `json:"features"`
}

// Supports reports whether the daemon advertises feature.
func (v VersionInfo) Supports(feature string) bool { return slices.Contains(v.Features, feature) }

// IncompatibleError is returned for every daemon request when the daemon's
// protocol is below MinDaemonProtocol.
type IncompatibleError struct {
	Daemon, Min int
}

func (e IncompatibleError) Error() string {
	return fmt.Sprintf("offscreend speaks protocol %d, but this offscreen needs protocol %d or later\nrestart the daemon with this build: offscreen daemon install", e.Daemon, e.Min)
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	respond(w, VersionInfo{Protocol: ProtocolVersion, Version: version.Build, Features: features})
}

// logProtocol logs, at debug, requests whose client protocol differs from
// the daemon's. Every request is still served: new behavior is opt-in per
// request, so older clients keep working.
func logProtocol(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(ProtocolHeader); got != strconv.Itoa(ProtocolVersion) {
			if got == "" {
				got = "none"
			}
			slog.Debug("client protocol differs from daemon", "client", got, "daemon", ProtocolVersion, "method", r.Method, "path", r.URL.Path)
		}
		next.ServeHTTP(w, r)
	})
}

// probe caches one daemon's VersionInfo for the life of a Client.
type probe struct {
	mu   sync.Mutex
	info *VersionInfo
}

// Version returns the daemon's protocol, build version and features. It
// asks the daemon once per Client and caches the answer; a daemon without
// GET /version answers 404 and is protocol 0 with no features. A failed
// request is not cached.
func (c Client) Version(ctx context.Context) (VersionInfo, error) {
	if c.probe == nil { // a Client not made by Dial: probe every time
		return c.fetchVersion(ctx)
	}
	c.probe.mu.Lock()
	defer c.probe.mu.Unlock()
	if c.probe.info != nil {
		return *c.probe.info, nil
	}
	info, err := c.fetchVersion(ctx)
	if err != nil {
		return VersionInfo{}, err
	}
	c.probe.info = &info
	return info, nil
}

func (c Client) fetchVersion(ctx context.Context) (VersionInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://offscreen/version", nil)
	if err != nil {
		return VersionInfo{}, err
	}
	req.Header.Set(ProtocolHeader, strconv.Itoa(ProtocolVersion))
	resp, err := c.http.Do(req)
	if err != nil {
		return VersionInfo{}, fmt.Errorf("daemon transport: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var info VersionInfo
	switch {
	case resp.StatusCode == http.StatusNotFound:
	case resp.StatusCode >= 400:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return VersionInfo{}, fmt.Errorf("daemon: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	default:
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return VersionInfo{}, fmt.Errorf("decode version response: %w", err)
		}
	}
	return info, nil
}

// compatible fails with IncompatibleError when the daemon is older than
// this client can use.
func (c Client) compatible(ctx context.Context) error {
	info, err := c.Version(ctx)
	if err != nil {
		return err
	}
	if info.Protocol < c.minProtocol {
		return IncompatibleError{Daemon: info.Protocol, Min: c.minProtocol}
	}
	return nil
}
