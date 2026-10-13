package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/version"
)

type VersionCmd struct{}

func (*VersionCmd) Run() error {
	client, err := daemon.Dial()
	if err != nil {
		return err
	}
	return printVersion(context.Background(), client, os.Stdout)
}

// printVersion prints this client's build and protocol, then the daemon's.
// A daemon that is not running is reported, not an error.
func printVersion(ctx context.Context, client daemon.Client, w io.Writer) error {
	if _, err := fmt.Fprintf(w, "client: %s (protocol %d)\n", version.Build, daemon.ProtocolVersion); err != nil {
		return err
	}
	info, err := client.Version(ctx)
	switch {
	case errors.Is(err, daemon.ErrUnavailable):
		_, err = fmt.Fprintln(w, "daemon: not running")
	case err != nil:
		return err
	case info.Protocol == 0:
		_, err = fmt.Fprintln(w, "daemon: protocol 0 (pre-negotiation)")
	default:
		_, err = fmt.Fprintf(w, "daemon: %s (protocol %d)\n", info.Version, info.Protocol)
	}
	return err
}

// warnOldDaemon prints one line on w when the daemon speaks an older
// protocol than this client but one it can still use, since some features
// stay off until the daemon is restarted with this build. Errors are left
// to the command's next request to report.
func warnOldDaemon(ctx context.Context, client daemon.Client, w io.Writer) {
	info, err := client.Version(ctx)
	if err != nil || info.Protocol >= daemon.ProtocolVersion || info.Protocol < daemon.MinDaemonProtocol {
		return
	}
	_, _ = fmt.Fprintf(w, "warning: offscreend speaks protocol %d, older than this offscreen's %d; restart it with this build to enable everything: offscreen daemon install\n", info.Protocol, daemon.ProtocolVersion)
}
