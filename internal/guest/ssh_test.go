package guest

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestDialReturnsUsableClientAfterContextCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(privateKey)
	require.NoError(t, err)
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
		return nil, nil
	}}
	serverConfig.AddHostKey(signer)
	serverErrors := make(chan error, 1)
	go func() {
		server, err := listener.Accept()
		if err != nil {
			serverErrors <- err
			return
		}
		defer func() { _ = server.Close() }()
		connection, channels, requests, err := ssh.NewServerConn(server, serverConfig)
		if err != nil {
			serverErrors <- err
			return
		}
		defer func() { _ = connection.Close() }()
		go func() {
			for channel := range channels {
				_ = channel.Reject(ssh.Prohibited, "no channels")
			}
		}()
		for request := range requests {
			if err := request.Reply(true, nil); err != nil {
				serverErrors <- err
				return
			}
		}
	}()

	cfg, err := (Credentials{User: "admin", Password: "test"}).dialConfig()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	client, err := dial(ctx, listener.Addr().String(), cfg)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()
	cancel()
	answered, _, err := client.SendRequest("keepalive@test", true, nil)
	require.NoError(t, err)
	require.True(t, answered, "canceling the dial context must not close a returned SSH client")
	select {
	case err := <-serverErrors:
		t.Fatalf("SSH server error: %v", err)
	default:
	}
}

func TestDialCancellationDuringSSHHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	cfg, err := (Credentials{User: "admin", Password: "test"}).dialConfig()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		client, err := dial(ctx, listener.Addr().String(), cfg)
		if client != nil {
			_ = client.Close()
		}
		result <- err
	}()
	server, err := listener.Accept()
	require.NoError(t, err)
	defer func() { _ = server.Close() }()
	require.NoError(t, server.SetReadDeadline(time.Now().Add(time.Second)))
	banner, err := bufio.NewReader(server).ReadString('\n')
	require.NoError(t, err)
	require.Contains(t, banner, "SSH-2.0-")
	// The peer has sent its SSH banner, so NewClientConn is now waiting for
	// ours. It must not outlive cancellation during that handshake.
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("SSH handshake did not return promptly after cancellation")
	}
}
