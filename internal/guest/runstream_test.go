package guest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// localSSH starts an SSH server on loopback that runs each exec request with
// the local /bin/sh and returns a client connected to it. Tests pass it only
// fixed scripts.
func localSSH(t *testing.T) *ssh.Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(privateKey)
	require.NoError(t, err)
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		for newChannel := range channels {
			channel, channelRequests, err := newChannel.Accept()
			if err != nil {
				continue
			}
			go serveExec(channel, channelRequests)
		}
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.InsecureIgnoreHostKey()}) // #nosec G106 -- loopback test server
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func serveExec(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = channel.Close() }()
	for request := range requests {
		if request.Type != "exec" || len(request.Payload) < 4 {
			_ = request.Reply(false, nil)
			continue
		}
		command := string(request.Payload[4:])
		_ = request.Reply(true, nil)
		cmd := exec.Command("/bin/sh", "-c", command) // #nosec G204 -- fixed test scripts only
		cmd.Stdin, cmd.Stdout, cmd.Stderr = channel, channel, channel.Stderr()
		status := uint32(0)
		if err := cmd.Run(); err != nil {
			status = 255
			if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
				status = uint32(exitErr.ExitCode()) // #nosec G115 -- exit codes are small
			}
		}
		payload := binary.BigEndian.AppendUint32(nil, status)
		_, _ = channel.SendRequest("exit-status", false, payload)
		return
	}
}

func TestRunStreamCopiesOutputAndKeepsItOutOfTheError(t *testing.T) {
	client := localSSH(t)
	var out strings.Builder
	err := RunStream(context.Background(), client, "read line; echo \"got $line\"; echo oops >&2; exit 3", strings.NewReader("input\n"), &out)
	require.Error(t, err)
	require.Equal(t, "guest command: Process exited with status 3", err.Error())
	require.Contains(t, out.String(), "got input\n")
	require.Contains(t, out.String(), "oops\n")
}

func TestRunStreamSucceeds(t *testing.T) {
	client := localSSH(t)
	var out strings.Builder
	require.NoError(t, RunStream(context.Background(), client, "echo ok", nil, &out))
	require.Equal(t, "ok\n", out.String())
}
