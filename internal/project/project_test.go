package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func sample(t *testing.T) Contract {
	t.Helper()
	cfg, err := LoadFile(filepath.Join("testdata", "sample", FileName))
	require.NoError(t, err)
	return cfg
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "nested", "deeper")
	require.NoError(t, os.MkdirAll(child, 0o700))
	const contract = "project = 'sample'\nexclusive = true\n[tasks.capture]\nlane = 'host'\nrun = 'true'\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, FileName), []byte(contract), 0o600))
	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, FileName), []byte(contract), 0o600))

	for _, tc := range []struct {
		name, start, worktree, project, wantRoot, wantError string
	}{
		{"ancestor search", child, "", "sample", root, ""},
		{"explicit worktree outside checkout", child, other, "sample", other, ""},
		{"explicit worktree does not search parents", child, child, "sample", "", "has no .offscreen.toml"},
		{"outside project", t.TempDir(), "", "sample", "", "not inside an offscreen project"},
		{"wrong project", root, "", "other", "", "does not match"},
		{"missing project", root, "", "", "", "project name is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Discover(tc.start, tc.worktree, tc.project)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantRoot, got.Root)
			require.Equal(t, "sample", got.Project)
			require.True(t, got.Exclusive)
			require.Equal(t, LaneHost, got.Tasks["capture"].Lane)
		})
	}
}

func TestLoadFileValidation(t *testing.T) {
	for _, tc := range []struct{ name, content, wantError string }{
		{"missing project", "[tasks.test]\nlane='vm'\nrun='true'\n", "missing project"},
		{"empty project", "project=' '\n", "missing project"},
		{"unknown root", "project='sample'\nextra=1\n", "unknown .offscreen.toml keys: extra"},
		{"unknown nested", "project='sample'\n[prepare]\nextra=1\n[guest]\nunknown=true\n[tasks.test]\nlane='vm'\nrun='true'\nother=1\n", "prepare.extra"},
		{"unknown guest", "project='sample'\n[guest]\nunknown=true\n", "guest.unknown"},
		{"unknown task key", "project='sample'\n[tasks.test]\nlane='vm'\nrun='true'\nother=1\n", "tasks.test.other"},
		{"invalid lane", "project='sample'\n[tasks.test]\nlane='remote'\nrun='true'\n", "invalid lane \"remote\" (valid: host, vm)"},
		{"missing task lane", "project='sample'\n[tasks.test]\nrun='true'\n", "invalid lane \"\""},
		{"missing task command", "project='sample'\n[tasks.test]\nlane='vm'\n", "run must be a nonempty"},
		{"empty share", "project='sample'\n[prepare]\nshare=['']\n", "prepare.share entries"},
		{"bad type", "project=123\n", "decode contract"},
		{"invalid toml", "project='sample'\nfoo=\n", "decode contract"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))
			_, err := LoadFile(path)
			require.ErrorContains(t, err, tc.wantError)
		})
	}
}

func TestResolve(t *testing.T) {
	cfg := sample(t)
	base := RunRequest{JobID: "j-42", OutDir: "/guest/out", CacheDir: "/guest/cache", HostEnv: map[string]string{"BUILD_DIR": "/host/build"}}
	for _, tc := range []struct {
		name      string
		task      string
		argv      []string
		lane      Lane
		wantLane  Lane
		wantShell string
		wantArgv  []string
	}{
		{"named vm", "test", nil, LaneAuto, LaneVM, "./scripts/test.sh", nil},
		{"named host", "capture", nil, LaneAuto, LaneHost, "./scripts/capture.sh", nil},
		{"override vm to host", "test", nil, LaneHost, LaneHost, "./scripts/test.sh", nil},
		{"override host to vm", "capture", nil, LaneVM, LaneVM, "./scripts/capture.sh", nil},
		{"direct argv", "", []string{"sh", "-c", "echo 'hello world'; exit 3"}, LaneAuto, LaneVM, "", []string{"sh", "-c", "echo 'hello world'; exit 3"}},
		{"direct host", "", []string{"printf", "%s", "a b"}, LaneHost, LaneHost, "", []string{"printf", "%s", "a b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			req.Task, req.Argv, req.Lane = tc.task, tc.argv, tc.lane
			got, err := Resolve(cfg, req)
			require.NoError(t, err)
			require.Equal(t, tc.wantLane, got.Lane)
			require.Equal(t, Command{Shell: tc.wantShell, Argv: tc.wantArgv}, got.Command)
			require.Equal(t, "sample", got.Project)
			require.Equal(t, cfg.Root, got.Root)
			require.True(t, got.Exclusive)
			require.Equal(t, "./scripts/build.sh", got.Prepare)
			require.Equal(t, "./scripts/setup.sh", got.GuestSetup)
			require.Equal(t, []string{"/host/build/app.bundle", filepath.Join(cfg.Root, "relative/assets")}, got.Shares)
			require.Equal(t, map[string]string{
				"OFFSCREEN_OUT": "/guest/out", "OFFSCREEN_JOB": "j-42",
				"OFFSCREEN_LANE": string(tc.wantLane), "OFFSCREEN_CACHE": "/guest/cache",
			}, got.Env)
		})
	}
}

func TestResolveErrors(t *testing.T) {
	cfg := sample(t)
	base := RunRequest{Task: "test", JobID: "j-1", OutDir: "/guest/out", CacheDir: "/guest/cache", HostEnv: map[string]string{"BUILD_DIR": "/host/build"}}
	for _, tc := range []struct {
		name   string
		modify func(*RunRequest)
		want   string
	}{
		{"unknown task", func(r *RunRequest) { r.Task = "bad" }, "unknown task \"bad\" (valid: capture, test)"},
		{"unknown lane", func(r *RunRequest) { r.Lane = "cloud" }, "invalid lane \"cloud\" (valid: auto, host, vm)"},
		{"missing invocation", func(r *RunRequest) { r.Task = "" }, "supply exactly one"},
		{"ambiguous invocation", func(r *RunRequest) { r.Argv = []string{"true"} }, "supply exactly one"},
		{"empty executable", func(r *RunRequest) { r.Task = ""; r.Argv = []string{""} }, "executable cannot be empty"},
		{"missing job", func(r *RunRequest) { r.JobID = "" }, "job ID, output directory and cache directory are required"},
		{"missing variable", func(r *RunRequest) { r.HostEnv = nil }, "unset host variable \"BUILD_DIR\""},
		{"empty expansion", func(r *RunRequest) {
			cfg.Prepare.Share = []string{"$BUILD_DIR"}
			r.HostEnv = map[string]string{"BUILD_DIR": ""}
		}, "expands to an empty path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			originalShares := cfg.Prepare.Share
			t.Cleanup(func() { cfg.Prepare.Share = originalShares })
			tc.modify(&req)
			_, err := Resolve(cfg, req)
			require.ErrorContains(t, err, tc.want)
		})
	}
}
