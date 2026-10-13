package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("offscreend is not running; run offscreen daemon install")

// Client talks to offscreend over its Unix socket. Copies share one cached
// version probe.
type Client struct {
	http        *http.Client
	probe       *probe
	minProtocol int
}

func Dial() (Client, error) {
	socket, err := Socket()
	if err != nil {
		return Client{}, err
	}
	return Client{http: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return conn, nil
	}}}, probe: &probe{}, minProtocol: MinDaemonProtocol}, nil
}

// call sends one request, after checking once per Client that the daemon's
// protocol is one this client can use.
func (c Client) call(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	if err := c.compatible(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://offscreen"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(ProtocolHeader, strconv.Itoa(ProtocolVersion))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("daemon transport: %w", err)
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("daemon: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

func (c Client) Submit(ctx context.Context, req Request) (Job, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return Job{}, err
	}
	resp, err := c.call(ctx, "POST", "/submit", bytes.NewReader(b))
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var job Job
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return Job{}, fmt.Errorf("decode submission response: %w", err)
	}
	if job.ID == "" || job.Artifacts == "" {
		return Job{}, errors.New("invalid submission response: missing job id or artifacts")
	}
	return job, nil
}

func (c Client) Jobs(ctx context.Context) ([]Job, error) {
	resp, err := c.call(ctx, "GET", "/jobs", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var jobs []Job
	err = json.NewDecoder(resp.Body).Decode(&jobs)
	return jobs, err
}

func (c Client) Job(ctx context.Context, id string) (Job, error) {
	resp, err := c.call(ctx, "GET", "/jobs/"+url.PathEscape(id), nil)
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var job Job
	err = json.NewDecoder(resp.Body).Decode(&job)
	return job, err
}

func (c Client) Cancel(ctx context.Context, id string) (Job, error) {
	resp, err := c.call(ctx, "POST", "/jobs/"+url.PathEscape(id)+"/cancel", nil)
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var job Job
	err = json.NewDecoder(resp.Body).Decode(&job)
	return job, err
}

// Prune previews or applies completed-job expiry in the daemon, which owns the
// job records and directories.
func (c Client) Prune(ctx context.Context, req PruneRequest) (PruneReport, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return PruneReport{}, err
	}
	resp, err := c.call(ctx, http.MethodPost, "/prune", bytes.NewReader(b))
	if err != nil {
		return PruneReport{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var report PruneReport
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return PruneReport{}, fmt.Errorf("decode prune response: %w", err)
	}
	return report, nil
}

// HostAction sends a user-initiated host-lane control action over the private socket.
func (c Client) HostAction(ctx context.Context, action, id string) (HostStatus, error) {
	path := "/host/" + action
	if id != "" {
		path += "/" + url.PathEscape(id)
	}
	resp, err := c.call(ctx, http.MethodPost, path, nil)
	if err != nil {
		return HostStatus{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var status HostStatus
	err = json.NewDecoder(resp.Body).Decode(&status)
	return status, err
}

func (c Client) Input(ctx context.Context, id string, input io.Reader) {
	resp, err := c.call(ctx, "POST", "/jobs/"+url.PathEscape(id)+"/input", input)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

// Stream replays from byte zero and follows until terminal, retaining stdout and
// stderr separately. A stream ending without a terminal status is a daemon error.
func (c Client) Stream(ctx context.Context, id string, stdout, stderr io.Writer) (Job, error) {
	return c.stream(ctx, id, stdout, stderr, nil)
}

// StreamPhases is Stream that also asks the daemon for phase frames and passes
// each one to phase, in order with the output. A nil phase is Stream. Check
// Version(ctx).Supports(FeaturePhases) first: a daemon from before phase
// frames ignores the request and sends output only.
func (c Client) StreamPhases(ctx context.Context, id string, stdout, stderr io.Writer, phase func(Phase)) (Job, error) {
	return c.stream(ctx, id, stdout, stderr, phase)
}

func (c Client) stream(ctx context.Context, id string, stdout, stderr io.Writer, phase func(Phase)) (Job, error) {
	path := "/jobs/" + url.PathEscape(id) + "/stream"
	if phase != nil {
		path += "?" + PhasesParam + "=1"
	}
	resp, err := c.call(ctx, "GET", path, nil)
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	for {
		kind, p, err := ReadFrame(resp.Body)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Job{}, fmt.Errorf("read job stream: %w", err)
		}
		switch {
		case kind == FrameStdout:
			_, err = stdout.Write(p)
		case kind == FrameStderr:
			_, err = stderr.Write(p)
		case kind == FramePhase && phase != nil:
			var event Phase
			if err := json.Unmarshal(p, &event); err != nil {
				return Job{}, fmt.Errorf("decode phase frame: %w", err)
			}
			phase(event)
		default:
			err = fmt.Errorf("invalid frame kind %q", kind)
		}
		if err != nil {
			return Job{}, fmt.Errorf("write job stream: %w", err)
		}
	}
	job, err := c.Job(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if !Terminal(job.State) {
		return job, fmt.Errorf("job stream ended before completion")
	}
	return job, nil
}

func (c Client) Logs(ctx context.Context, id string, follow bool, stdout, stderr io.Writer) error {
	if follow {
		_, err := c.Stream(ctx, id, stdout, stderr)
		return err
	}
	job, err := c.Job(ctx, id)
	if err != nil {
		return err
	}
	f, err := os.Open(job.Artifacts + "/stream.bin")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	for {
		kind, p, err := ReadFrame(f)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if kind == FrameStdout {
			_, err = stdout.Write(p)
		} else {
			_, err = stderr.Write(p)
		}
		if err != nil {
			return err
		}
	}
}

func (c Client) Wait(ctx context.Context, id string) (Job, error) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		job, err := c.Job(ctx, id)
		if err != nil {
			return job, err
		}
		if Terminal(job.State) {
			return job, nil
		}
		select {
		case <-ctx.Done():
			return Job{}, ctx.Err()
		case <-t.C:
		}
	}
}
