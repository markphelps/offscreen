package daemon

import (
	"context"
	"encoding/xml"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func installerFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	path := filepath.Join(root, "LaunchAgents", Label+".plist")
	source := filepath.Join(root, "offscreend")
	require.NoError(t, os.WriteFile(source, []byte("new daemon"), 0o600))
	return dir, path, source
}

func oldInstallation(t *testing.T, dir, path string) []byte {
	t.Helper()
	old := []byte(`<?xml version="1.0"?><plist><dict><key>Label</key><string>` + Label + `</string><key>ProgramArguments</key><array><string>legacy</string></array></dict></plist>`)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, old, 0o600))
	bundle := filepath.Join(dir, bundleName, "Contents")
	require.NoError(t, os.MkdirAll(filepath.Join(bundle, "MacOS"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "Info.plist"), infoPlist(), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "MacOS", "offscreend"), []byte("old daemon"), 0o600))
	return old
}

func readyImmediately(context.Context) error { return nil }

func missingService() []byte {
	return []byte("Bad request.\n" + `Could not find service "` + Label + `" in domain for user gui: 501`)
}

func TestInstallPublishesBundleAndLaunchAgent(t *testing.T) {
	dir, path, source := installerFixture(t)
	var commands []string
	run := func(name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	env := map[string]string{"PATH": "/opt/tart:&<path>", "OFFSCREEN_CONFIG": "some&<config>", "OFFSCREEN_STATE_DIR": "state"}
	require.NoError(t, install(dir, path, source, "gui/501", env, run, readyImmediately))
	bundle := filepath.Join(dir, bundleName)
	binary := filepath.Join(bundle, "Contents", "MacOS", "offscreend")
	contents, err := os.ReadFile(binary)
	require.NoError(t, err)
	require.Equal(t, "new daemon", string(contents))
	metadata, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	require.NoError(t, err)
	require.Equal(t, Label, mustPlistValue(t, metadata, "CFBundleIdentifier"))
	require.Equal(t, "offscreend", mustPlistValue(t, metadata, "CFBundleExecutable"))
	require.Equal(t, "Offscreen Daemon", mustPlistValue(t, metadata, "CFBundleDisplayName"))
	require.Equal(t, "APPL", mustPlistValue(t, metadata, "CFBundlePackageType"))
	require.Equal(t, "1.0", mustPlistValue(t, metadata, "CFBundleShortVersionString"))
	require.Contains(t, string(metadata), "<key>LSUIElement</key><true/>")
	require.Contains(t, mustPlistValue(t, metadata, "NSLocalNetworkUsageDescription"), "Tart virtual machines")
	plist, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, Label, mustPlistValue(t, plist, "Label"))
	require.Contains(t, string(plist), "<key>ProgramArguments</key><array><string>"+xmlString(binary)+"</string></array>")
	require.Contains(t, string(plist), "<key>AssociatedBundleIdentifiers</key><array><string>"+Label+"</string></array>")
	require.Contains(t, string(plist), "<key>PATH</key><string>/opt/tart:&amp;&lt;path&gt;</string>")
	require.Contains(t, string(plist), "<key>OFFSCREEN_CONFIG</key><string>some&amp;&lt;config&gt;</string>")
	var parsed any
	require.NoError(t, xml.Unmarshal(plist, &parsed))
	require.Len(t, commands, 5)
	require.Contains(t, commands[0], "/usr/bin/codesign --force --sign - --identifier "+Label)
	require.Contains(t, commands[0], `=designated => identifier "`+Label+`"`)
	require.Contains(t, commands[1], "/usr/bin/codesign --verify --strict")
	require.Contains(t, commands[2], lsregister+" -f ")
	require.Contains(t, commands[3], lsregister+" -f "+bundle)
	require.Equal(t, "launchctl bootstrap gui/501 "+path, commands[4])
}

func mustPlistValue(t *testing.T, data []byte, key string) string {
	t.Helper()
	value, err := plistValue(data, key)
	require.NoError(t, err)
	return value
}

func TestInstallPreflightFailureLeavesExistingServiceUntouched(t *testing.T) {
	for _, failed := range []string{"--sign", "--verify", lsregister} {
		t.Run(failed, func(t *testing.T) {
			dir, path, source := installerFixture(t)
			old := oldInstallation(t, dir, path)
			var commands []string
			run := func(name string, args ...string) ([]byte, error) {
				command := name + " " + strings.Join(args, " ")
				commands = append(commands, command)
				if strings.Contains(command, failed) {
					return []byte("simulated failure"), errors.New("exit status 1")
				}
				return nil, nil
			}
			err := install(dir, path, source, "gui/501", nil, run, readyImmediately)
			require.ErrorContains(t, err, "simulated failure")
			require.NotContains(t, strings.Join(commands, "\n"), "launchctl bootout")
			actual, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.Equal(t, old, actual)
			binary, readErr := os.ReadFile(filepath.Join(dir, bundleName, "Contents", "MacOS", "offscreend"))
			require.NoError(t, readErr)
			require.Equal(t, "old daemon", string(binary))
		})
	}
}

func TestInstallRegistrationFailureAfterPublishRestoresPreviousFiles(t *testing.T) {
	dir, path, source := installerFixture(t)
	old := oldInstallation(t, dir, path)
	registrations := 0
	loaded := true
	var commands []string
	run := func(name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		if name == "launchctl" {
			switch args[0] {
			case "print":
				if !loaded {
					return missingService(), errors.New("exit status 113")
				}
			case "bootout":
				loaded = false
			case "bootstrap":
				loaded = true
			}
		}
		if name == lsregister {
			registrations++
			if registrations == 2 {
				return []byte("registration rejected"), errors.New("exit status 1")
			}
		}
		return nil, nil
	}
	err := install(dir, path, source, "gui/501", nil, run, readyImmediately)
	require.ErrorContains(t, err, "registration rejected")
	require.Equal(t, 3, registrations, "old bundle registration must be restored")
	require.Contains(t, strings.Join(commands, "\n"), "launchctl bootstrap gui/501 "+path)
	actual, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, old, actual)
	binary, readErr := os.ReadFile(filepath.Join(dir, bundleName, "Contents", "MacOS", "offscreend"))
	require.NoError(t, readErr)
	require.Equal(t, "old daemon", string(binary))
}

func TestInstallReinstallAndBootstrapRollback(t *testing.T) {
	dir, path, source := installerFixture(t)
	old := oldInstallation(t, dir, path)
	calls := 0
	loaded := true
	run := func(name string, args ...string) ([]byte, error) {
		if name == "launchctl" {
			switch args[0] {
			case "print":
				if !loaded {
					return missingService(), errors.New("exit status 113")
				}
			case "bootout":
				loaded = false
			case "bootstrap":
				calls++
				if calls == 1 {
					return []byte("new daemon rejected"), errors.New("exit status 5")
				}
				loaded = true
			}
		}
		return nil, nil
	}
	err := install(dir, path, source, "gui/501", map[string]string{"PATH": "/usr/bin"}, run, readyImmediately)
	require.ErrorContains(t, err, "new daemon rejected")
	require.Equal(t, 2, calls, "rollback must restart the previous service")
	actual, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, old, actual)
	binary, readErr := os.ReadFile(filepath.Join(dir, bundleName, "Contents", "MacOS", "offscreend"))
	require.NoError(t, readErr)
	require.Equal(t, "old daemon", string(binary))
	// A successful reinstall then points launchd at the stable bundle, not a stage directory.
	require.NoError(t, install(dir, path, source, "gui/501", map[string]string{"PATH": "/usr/bin"}, run, readyImmediately))
	actual, readErr = os.ReadFile(path)
	require.NoError(t, readErr)
	require.Contains(t, string(actual), xmlString(filepath.Join(dir, bundleName, "Contents", "MacOS", "offscreend")))
	binary, readErr = os.ReadFile(filepath.Join(dir, bundleName, "Contents", "MacOS", "offscreend"))
	require.NoError(t, readErr)
	require.Equal(t, "new daemon", string(binary))
}

func TestInstallRefusesForegroundDaemonBeforeStaging(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "offscreen-foreground-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	dir := filepath.Join(root, "state")
	path := filepath.Join(root, "LaunchAgents", Label+".plist")
	source := filepath.Join(root, "offscreend")
	require.NoError(t, os.WriteFile(source, []byte("new daemon"), 0o600))
	old := oldInstallation(t, dir, path)
	socket := filepath.Join(dir, "offscreend.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jobs" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	getJobs := func() {
		t.Helper()
		resp, err := client.Get("http://offscreen/jobs")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	}
	getJobs()
	var commands []string
	run := func(name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		if name == "launchctl" && args[0] == "print" {
			return missingService(), errors.New("exit status 113")
		}
		return nil, nil
	}
	readinessCalls := 0
	err = installWithWait(dir, path, source, "gui/501", nil, run, func(context.Context) error {
		readinessCalls++
		return nil // The foreground daemon could have satisfied this erroneously.
	}, 30*time.Millisecond)
	require.ErrorContains(t, err, "daemon socket busy without a loaded LaunchAgent")
	require.Zero(t, readinessCalls)
	require.Equal(t, []string{"launchctl print gui/501/" + Label}, commands, "preflight must not sign, register or bootstrap")
	plist, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, old, plist)
	binary, readErr := os.ReadFile(filepath.Join(dir, bundleName, "Contents", "MacOS", "offscreend"))
	require.NoError(t, readErr)
	require.Equal(t, "old daemon", string(binary))
	getJobs() // The installer did not signal or stop the foreground daemon.
}

func TestInstallWaitsForWorkingDaemonAPI(t *testing.T) {
	dir, path, source := installerFixture(t)
	bootstrapped := false
	attempts := 0
	run := func(name string, args ...string) ([]byte, error) {
		if name == "launchctl" && args[0] == "bootstrap" {
			bootstrapped = true
		}
		return nil, nil
	}
	ready := func(context.Context) error {
		require.True(t, bootstrapped, "readiness check must follow bootstrap")
		attempts++
		if attempts < 3 {
			return errors.New("socket not accepting API requests yet")
		}
		return nil
	}
	require.NoError(t, installWithWait(dir, path, source, "gui/501", nil, run, ready, time.Second))
	require.Equal(t, 3, attempts)
}

func TestInstallReadinessFailureRollsBackLoadedDaemon(t *testing.T) {
	dir, path, source := installerFixture(t)
	old := oldInstallation(t, dir, path)
	loaded := true
	bootstraps := 0
	bootouts := 0
	run := func(name string, args ...string) ([]byte, error) {
		if name == "launchctl" {
			switch args[0] {
			case "print":
				if !loaded {
					return missingService(), errors.New("exit status 113")
				}
			case "bootout":
				bootouts++
				loaded = false
			case "bootstrap":
				bootstraps++
				loaded = true
			}
		}
		return nil, nil
	}
	err := installWithWait(dir, path, source, "gui/501", nil, run, func(context.Context) error {
		return errors.New("HTTP Jobs unavailable")
	}, 180*time.Millisecond)
	require.ErrorContains(t, err, "daemon did not become ready")
	require.ErrorContains(t, err, "HTTP Jobs unavailable")
	require.Equal(t, 2, bootstraps, "rollback restarts previously running daemon")
	require.Equal(t, 2, bootouts, "old and failed replacement must both stop")
	require.True(t, loaded)
	plist, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, old, plist)
	binary, readErr := os.ReadFile(filepath.Join(dir, bundleName, "Contents", "MacOS", "offscreend"))
	require.NoError(t, readErr)
	require.Equal(t, "old daemon", string(binary))
}

func TestInstallReadinessFailurePreservesStoppedOldAgent(t *testing.T) {
	dir, path, source := installerFixture(t)
	old := oldInstallation(t, dir, path)
	loaded := false
	bootstraps := 0
	run := func(name string, args ...string) ([]byte, error) {
		if name == "launchctl" {
			switch args[0] {
			case "print":
				if !loaded {
					return missingService(), errors.New("exit status 113")
				}
			case "bootstrap":
				bootstraps++
				loaded = true
			case "bootout":
				loaded = false
			}
		}
		return nil, nil
	}
	err := installWithWait(dir, path, source, "gui/501", nil, run, func(context.Context) error {
		return errors.New("HTTP Jobs unavailable")
	}, 130*time.Millisecond)
	require.ErrorContains(t, err, "daemon did not become ready")
	require.Equal(t, 1, bootstraps, "old stopped service must not start on rollback")
	require.False(t, loaded)
	plist, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, old, plist)
	binary, readErr := os.ReadFile(filepath.Join(dir, bundleName, "Contents", "MacOS", "offscreend"))
	require.NoError(t, readErr)
	require.Equal(t, "old daemon", string(binary))
}

func TestInitialInstallReadinessFailureRemovesNewServiceFiles(t *testing.T) {
	dir, path, source := installerFixture(t)
	loaded := false
	bootouts := 0
	run := func(name string, args ...string) ([]byte, error) {
		if name == "launchctl" {
			switch args[0] {
			case "print":
				if !loaded {
					return missingService(), errors.New("exit status 113")
				}
			case "bootstrap":
				loaded = true
			case "bootout":
				loaded = false
				bootouts++
			}
		}
		return nil, nil
	}
	err := installWithWait(dir, path, source, "gui/501", nil, run, func(context.Context) error {
		return errors.New("HTTP Jobs unavailable")
	}, 130*time.Millisecond)
	require.ErrorContains(t, err, "daemon did not become ready")
	require.Equal(t, 1, bootouts)
	require.False(t, loaded)
	_, statErr := os.Stat(path)
	require.ErrorIs(t, statErr, os.ErrNotExist)
	_, statErr = os.Stat(filepath.Join(dir, bundleName))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestInstallWaitsForOldLabelToDisappear(t *testing.T) {
	dir, path, source := installerFixture(t)
	oldInstallation(t, dir, path)
	stopping := false
	checksAfterBootout := 0
	run := func(name string, args ...string) ([]byte, error) {
		if name == "launchctl" {
			switch args[0] {
			case "print":
				if stopping {
					checksAfterBootout++
					if checksAfterBootout >= 3 {
						return missingService(), errors.New("exit status 113")
					}
				}
			case "bootout":
				stopping = true
			}
		}
		return nil, nil
	}
	ready := func(context.Context) error {
		require.GreaterOrEqual(t, checksAfterBootout, 3, "new daemon cannot count old socket as ready")
		return nil
	}
	require.NoError(t, installWithWait(dir, path, source, "gui/501", nil, run, ready, time.Second))
}

func TestWaitServiceGoneDoesNotIgnorePrintFailureOrHang(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Millisecond)
	defer cancel()
	err := waitServiceGone(ctx, func(string, ...string) ([]byte, error) { return nil, nil }, "gui/501")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	err = waitServiceGone(context.Background(), func(string, ...string) ([]byte, error) {
		return []byte("Operation not permitted"), errors.New("exit status 1")
	}, "gui/501")
	require.ErrorContains(t, err, "Operation not permitted")
}

func TestWaitOldSocketGoneDoesNotAcceptPreviousAPI(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "offscreen-socket-test-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	listener, err := net.Listen("unix", filepath.Join(dir, "offscreend.sock"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 140*time.Millisecond)
	defer cancel()
	err = waitOldSocketGone(ctx, dir)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, listener.Close())
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, waitOldSocketGone(ctx, dir), "a stale but non-listening socket is safe")
}

func TestUninstallTouchesOnlyOwnedLaunchAgent(t *testing.T) {
	dir, path, _ := installerFixture(t)
	oldInstallation(t, dir, path)
	calls := 0
	run := func(name string, args ...string) ([]byte, error) {
		calls++
		require.Equal(t, "launchctl", name)
		if args[0] == "print" {
			require.Equal(t, []string{"print", "gui/501/" + Label}, args)
			return nil, nil
		}
		require.Equal(t, []string{"bootout", "gui/501/" + Label}, args)
		return nil, nil
	}
	require.NoError(t, os.WriteFile(path, []byte(`<plist><dict><key>Label</key><string>another.agent</string></dict></plist>`), 0o600))
	require.ErrorContains(t, uninstall(path, "gui/501", run), "not owned")
	require.Zero(t, calls)
	require.NoError(t, os.WriteFile(path, agentPlist(dir, filepath.Join(dir, bundleName), nil), 0o600))
	require.NoError(t, uninstall(path, "gui/501", run))
	require.Equal(t, 2, calls)
	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(dir, bundleName, "Contents", "MacOS", "offscreend"))
	require.NoError(t, err, "uninstall retains its old bundle")
}

func TestInstallStoppedAgentUpgradeAndRollbackStayStopped(t *testing.T) {
	for _, failBootstrap := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "bootstrap_failure"}[failBootstrap], func(t *testing.T) {
			dir, path, source := installerFixture(t)
			old := oldInstallation(t, dir, path)
			var commands []string
			run := func(name string, args ...string) ([]byte, error) {
				command := name + " " + strings.Join(args, " ")
				commands = append(commands, command)
				if name == "launchctl" && args[0] == "print" {
					return []byte("Bad request.\n" + `Could not find service "` + Label + `" in domain for user gui: 501`), errors.New("exit status 113")
				}
				if failBootstrap && name == "launchctl" && args[0] == "bootstrap" {
					return []byte("failed replacement"), errors.New("exit status 5")
				}
				return nil, nil
			}
			err := install(dir, path, source, "gui/501", nil, run, readyImmediately)
			if failBootstrap {
				require.ErrorContains(t, err, "failed replacement")
				plist, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				require.Equal(t, old, plist)
				binary, readErr := os.ReadFile(filepath.Join(dir, bundleName, "Contents", "MacOS", "offscreend"))
				require.NoError(t, readErr)
				require.Equal(t, "old daemon", string(binary))
			} else {
				require.NoError(t, err)
			}
			joined := strings.Join(commands, "\n")
			require.NotContains(t, joined, "launchctl bootout", "a stopped service must not be stopped again")
			require.Equal(t, 1, strings.Count(joined, "launchctl bootstrap"), "rollback must not start a previously stopped service")
		})
	}
}

func TestInstallInspectionOrBootoutFailurePreservesExistingAgent(t *testing.T) {
	for _, fail := range []string{"print", "bootout"} {
		t.Run(fail, func(t *testing.T) {
			dir, path, source := installerFixture(t)
			old := oldInstallation(t, dir, path)
			var commands []string
			run := func(name string, args ...string) ([]byte, error) {
				command := name + " " + strings.Join(args, " ")
				commands = append(commands, command)
				if name == "launchctl" && args[0] == fail {
					return []byte("Operation not permitted"), errors.New("exit status 1")
				}
				return nil, nil
			}
			err := install(dir, path, source, "gui/501", nil, run, readyImmediately)
			require.ErrorContains(t, err, "Operation not permitted")
			require.NotContains(t, strings.Join(commands, "\n"), "launchctl bootstrap")
			plist, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.Equal(t, old, plist)
			binary, readErr := os.ReadFile(filepath.Join(dir, bundleName, "Contents", "MacOS", "offscreend"))
			require.NoError(t, readErr)
			require.Equal(t, "old daemon", string(binary))
		})
	}
}

func TestUninstallStoppedOrFailedStop(t *testing.T) {
	for _, mode := range []string{"stopped", "permission"} {
		t.Run(mode, func(t *testing.T) {
			dir, path, _ := installerFixture(t)
			old := oldInstallation(t, dir, path)
			var commands []string
			run := func(name string, args ...string) ([]byte, error) {
				commands = append(commands, name+" "+strings.Join(args, " "))
				if mode == "stopped" && args[0] == "print" {
					return []byte("Bad request.\n" + `Could not find service "` + Label + `" in domain for user gui: 501`), errors.New("exit status 113")
				}
				if mode == "permission" && args[0] == "bootout" {
					return []byte("Operation not permitted"), errors.New("exit status 1")
				}
				return nil, nil
			}
			err := uninstall(path, "gui/501", run)
			if mode == "stopped" {
				require.NoError(t, err)
				require.Len(t, commands, 1)
				_, err = os.Stat(path)
				require.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.ErrorContains(t, err, "Operation not permitted")
				plist, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				require.Equal(t, old, plist)
			}
		})
	}
}

func TestServiceLoadedRejectsMissingDomainAndPermissionFailures(t *testing.T) {
	for _, message := range []string{"Could not find domain for user gui: 501", "Operation not permitted", "Could not find service \"another.label\" in domain for user gui: 501"} {
		loaded, err := serviceLoaded(func(string, ...string) ([]byte, error) {
			return []byte(message), errors.New("exit status 1")
		}, "gui/501")
		require.False(t, loaded)
		require.ErrorContains(t, err, message)
	}
}

func TestInstallRefusesForeignFilesBeforeStoppingService(t *testing.T) {
	dir, path, source := installerFixture(t)
	oldInstallation(t, dir, path)
	require.NoError(t, os.WriteFile(path, []byte(`<plist><dict><key>Label</key><string>another.agent</string></dict></plist>`), 0o600))
	called := false
	err := install(dir, path, source, "gui/501", nil, func(string, ...string) ([]byte, error) {
		called = true
		return nil, nil
	}, readyImmediately)
	require.ErrorContains(t, err, "not owned")
	require.False(t, called)
}
