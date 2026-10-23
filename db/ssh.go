package db

// SSH tunnel support: when a connection enables SSH, the host opens an SSH
// client to a bastion and starts a local listener that forwards to the real
// database Host:Port. The database driver (built-in MySQL or any plugin) then
// connects to 127.0.0.1:<LocalPort>, never seeing the bastion. This keeps the
// feature uniform across every engine and out of the plugin protocol.

import (
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshDialer is the real SSH dial; overridable in tests.
var sshDialer = ssh.Dial

// SSHConfig carries the bastion connection settings. When Enabled, dialConn
// opens a tunnel and rewrites the database Host/Port to the local forward.
type SSHConfig struct {
	Enabled       bool   `json:"enabled,omitempty"`
	Host          string `json:"host,omitempty"`
	Port          string `json:"port,omitempty"`
	User          string `json:"user,omitempty"`
	AuthMode      string `json:"authMode,omitempty"` // "password" | "key" (default "key")
	Password      string `json:"password,omitempty"`
	KeyPath       string `json:"keyPath,omitempty"`
	KeyPassphrase string `json:"keyPassphrase,omitempty"`
}

// sshTunnel is a local-port forward over a single SSH connection.
type sshTunnel struct {
	client    *ssh.Client
	listener  net.Listener
	localPort int
	closed    sync.Once
}

// OpenTunnel dials the bastion and starts a local listener forwarding to
// remoteHost:remotePort. The returned tunnel's LocalPort is where the database
// driver should connect (127.0.0.1:LocalPort).
func OpenTunnel(cfg SSHConfig, remoteHost, remotePort string) (*sshTunnel, error) {
	return openTunnel(cfg, remoteHost, remotePort)
}

func openTunnel(cfg SSHConfig, remoteHost, remotePort string) (*sshTunnel, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("ssh: no bastion host configured")
	}
	if remoteHost == "" {
		return nil, fmt.Errorf("ssh: no remote database host")
	}
	port := cfg.Port
	if port == "" {
		port = "22"
	}
	auths, err := sshAuthMethods(cfg)
	if err != nil {
		return nil, err
	}
	sshCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // local dev tool; TODO allow a known_hosts file
		Timeout:         10 * time.Second,
	}
	addr := net.JoinHostPort(cfg.Host, port)
	client, err := sshDialer("tcp", addr, sshCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("ssh local listener: %w", err)
	}
	t := &sshTunnel{
		client:    client,
		listener:  listener,
		localPort: listener.Addr().(*net.TCPAddr).Port,
	}
	go t.acceptLoop(net.JoinHostPort(remoteHost, remotePort))
	return t, nil
}

func (t *sshTunnel) acceptLoop(remoteAddr string) {
	for {
		local, err := t.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go t.forward(local, remoteAddr)
	}
}

func (t *sshTunnel) forward(local net.Conn, remoteAddr string) {
	defer local.Close()
	remote, err := t.client.Dial("tcp", remoteAddr)
	if err != nil {
		return
	}
	defer remote.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	<-done
}

// LocalPort is the address (127.0.0.1:<LocalPort>) the database driver should
// connect to.
func (t *sshTunnel) LocalPort() int { return t.localPort }

// Close shuts down the listener and SSH client exactly once.
func (t *sshTunnel) Close() {
	t.closed.Do(func() {
		if t.listener != nil {
			_ = t.listener.Close()
		}
		if t.client != nil {
			_ = t.client.Close()
		}
	})
}

// NewTunneledSession wraps a live session with its SSH tunnel so that Close
// tears down both, and forwards the optional capability interfaces.
func NewTunneledSession(inner Session, t *sshTunnel) Session {
	return &tunneledSession{capabilityWrapper: &capabilityWrapper{inner}, tunnel: t}
}

type tunneledSession struct {
	*capabilityWrapper
	tunnel *sshTunnel
}

func (s *tunneledSession) Close() {
	s.Session.Close() // inner database session
	s.tunnel.Close()
}

func sshAuthMethods(cfg SSHConfig) ([]ssh.AuthMethod, error) {
	switch cfg.AuthMode {
	case "password":
		return []ssh.AuthMethod{ssh.Password(cfg.Password)}, nil
	case "key", "":
		if cfg.KeyPath == "" {
			return nil, fmt.Errorf("ssh: key auth requires a private key path")
		}
		key, err := os.ReadFile(cfg.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("ssh read key: %w", err)
		}
		var signer ssh.Signer
		if cfg.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(cfg.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, fmt.Errorf("ssh parse key: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, fmt.Errorf("ssh: unknown auth mode %q", cfg.AuthMode)
	}
}
