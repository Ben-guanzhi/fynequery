package db

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"fynequery/proto"

	"golang.org/x/crypto/ssh"
)

// --- helpers -------------------------------------------------------------

// startEchoTarget is a TCP server that echoes bytes (stands in for the DB the
// tunnel forwards to).
func startEchoTarget(t *testing.T) (string, func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go echoConn(conn)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func echoConn(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 1024)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			if _, werr := c.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// startSSHServer runs a minimal SSH server that forwards direct-tcpip channels
// to targetAddr. It accepts only the given client public key.
func startSSHServer(t *testing.T, hostSigner ssh.Signer, clientPub ssh.PublicKey, targetAddr string) (string, func()) {
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) == string(clientPub.Marshal()) {
			return &ssh.Permissions{}, nil
		}
		return nil, fmt.Errorf("denied")
	}}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			tcp, err := ln.Accept()
			if err != nil {
				return
			}
			go func(tc net.Conn) {
				_, chans, reqs, err := ssh.NewServerConn(tc, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					if ch.ChannelType() != "direct-tcpip" {
						_ = ch.Reject(ssh.UnknownChannelType, "unknown")
						continue
					}
					var req struct {
						HostToConnect  string
						PortToConnect  uint32
						OriginatorHost string
						OriginatorPort uint32
					}
					if err := ssh.Unmarshal(ch.ExtraData(), &req); err != nil {
						_ = ch.Reject(ssh.ConnectionFailed, "bad payload")
						continue
					}
					remote, err := net.Dial("tcp", net.JoinHostPort(req.HostToConnect, strconv.Itoa(int(req.PortToConnect))))
					if err != nil {
						_ = ch.Reject(ssh.ConnectionFailed, err.Error())
						continue
					}
					chChan, chReqs, err := ch.Accept()
					if err != nil {
						_ = remote.Close()
						continue
					}
					go ssh.DiscardRequests(chReqs)
					go forwardChannel(chChan, remote)
				}
			}(tcp)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func forwardChannel(ch ssh.Channel, remote net.Conn) {
	defer ch.Close()
	defer remote.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, ch); done <- struct{}{} }()
	go func() { _, _ = io.Copy(ch, remote); done <- struct{}{} }()
	<-done
}

func genKey(t *testing.T) (ssh.Signer, []byte) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return signer, pemBytes
}

func writeTempFile(t *testing.T, data []byte) string {
	path := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func hostPort(addr string) (string, int) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0
	}
	port, _ := strconv.Atoi(p)
	return h, port
}

// --- tests ----------------------------------------------------------------

// TestOpenTunnelForwards verifies a real SSH tunnel forwards TCP bytes to the
// target database (here, an echo server) and authenticates with a key.
func TestOpenTunnelForwards(t *testing.T) {
	targetAddr, stopTarget := startEchoTarget(t)
	defer stopTarget()

	hostSigner, clientPEM := genKey(t)
	clientSigner, err := ssh.ParsePrivateKey(clientPEM)
	if err != nil {
		t.Fatal(err)
	}
	serverAddr, stopServer := startSSHServer(t, hostSigner, clientSigner.PublicKey(), targetAddr)
	defer stopServer()

	keyFile := writeTempFile(t, clientPEM)
	sHost, sPort := hostPort(serverAddr)
	_, tgtPort := hostPort(targetAddr)
	tPort := strconv.Itoa(tgtPort)

	cfg := SSHConfig{
		Enabled:  true,
		Host:     sHost,
		Port:     strconv.Itoa(sPort),
		User:     "tester",
		AuthMode: "key",
		KeyPath:  keyFile,
	}
	tunnel, err := OpenTunnel(cfg, "127.0.0.1", tPort)
	if err != nil {
		t.Fatalf("openTunnel: %v", err)
	}
	defer tunnel.Close()

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.LocalPort()))
	if err != nil {
		t.Fatalf("dial local forward: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo mismatch: %q", buf)
	}
}

// TestOpenTunnelEmptyBastion checks the obvious validation guard.
func TestOpenTunnelEmptyBastion(t *testing.T) {
	if _, err := OpenTunnel(SSHConfig{Enabled: true}, "127.0.0.1", "5432"); err == nil {
		t.Fatal("expected error for missing bastion host")
	}
}

// --- capability forwarding ------------------------------------------------

type fakeUpdaterSession struct{}

func (fakeUpdaterSession) Server() string                             { return "" }
func (fakeUpdaterSession) Tables() ([]proto.ObjectInfo, error)        { return nil, nil }
func (fakeUpdaterSession) Columns(string) ([]proto.ColumnInfo, error) { return nil, nil }
func (fakeUpdaterSession) Query(string, int) (proto.QueryResult, error) {
	return proto.QueryResult{}, nil
}
func (fakeUpdaterSession) Browse(string, int) (proto.QueryResult, error) {
	return proto.QueryResult{}, nil
}
func (fakeUpdaterSession) Close() {}
func (fakeUpdaterSession) UpdateRow(string, map[string]*string, map[string]*string) error {
	return nil
}
func (fakeUpdaterSession) DeleteRow(string, map[string]*string) error { return nil }
func (fakeUpdaterSession) InsertRow(string, map[string]*string) error { return nil }
func (fakeUpdaterSession) ApplyChanges(string, []RowEdit, []map[string]*string, []map[string]*string, []string) error {
	return nil
}

type fakePlainSession struct{}

func (fakePlainSession) Server() string                             { return "" }
func (fakePlainSession) Tables() ([]proto.ObjectInfo, error)        { return nil, nil }
func (fakePlainSession) Columns(string) ([]proto.ColumnInfo, error) { return nil, nil }
func (fakePlainSession) Query(string, int) (proto.QueryResult, error) {
	return proto.QueryResult{}, nil
}
func (fakePlainSession) Browse(string, int) (proto.QueryResult, error) {
	return proto.QueryResult{}, nil
}
func (fakePlainSession) Close() {}

func TestCapabilityForwarding(t *testing.T) {
	// A bare type assertion on a wrapper is always true (the wrapper declares
	// every forwarding method), so capability detection MUST go through As*.
	w := wrapCapabilities(fakeUpdaterSession{})
	if _, ok := AsUpdater(w); !ok {
		t.Fatal("Updater should be forwarded")
	}
	if _, ok := AsBatchUpdater(w); !ok {
		t.Fatal("BatchUpdater should be forwarded")
	}
	if _, ok := AsCanceller(w); ok {
		t.Fatal("Canceller should NOT be forwarded for a plain updater")
	}
	u, _ := AsUpdater(w)
	if err := u.UpdateRow("t", nil, nil); err != nil {
		t.Fatalf("forwarded UpdateRow: %v", err)
	}

	// A plain inner session exposes nothing extra: AsUpdater reports false so
	// callers never call UpdateRow through it, and the wrapper's forwarding
	// guard still returns errUnsupported if the method is ever reached.
	p := wrapCapabilities(fakePlainSession{})
	if _, ok := AsUpdater(p); ok {
		t.Fatal("Updater should NOT be forwarded for a plain session")
	}
	if err := p.(Updater).UpdateRow("t", nil, nil); err == nil {
		t.Fatal("expected unsupported error")
	}
}

func TestTunneledSessionForwardsCapabilities(t *testing.T) {
	s := NewTunneledSession(fakeUpdaterSession{}, &sshTunnel{})
	tu, ok := AsUpdater(s)
	if !ok {
		t.Fatal("tunneled session should expose Updater")
	}
	if err := tu.UpdateRow("t", nil, nil); err != nil {
		t.Fatalf("tunneled UpdateRow: %v", err)
	}
}
