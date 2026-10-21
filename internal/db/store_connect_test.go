package db

import (
	"context"
	"testing"
	"time"
)

// TestTestConnectionHonorCancel pins the whole point of ctx: against a host
// that never answers (a non-routable address, so the dial just sits there) a
// canceled context must make TestConnection give up quickly instead of
// hanging the 停止 button until the OS times out the dial. MySQL is builtin, so
// this exercises the in-process Engine path's cancellation.
func TestTestConnectionHonorCancel(t *testing.T) {
	// 10.255.255.1 is RFC1918 and black-holed on ordinary networks: a TCP
	// connect to it neither succeeds nor gets a fast RST, so without
	// cancellation the handshake would outlive any reasonable wait.
	cfg := ConnConfig{
		Type:     DbTypeMySQL,
		Host:     "10.255.255.1",
		Port:     3306,
		Username: "u",
		Password: "p",
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	e, err := TestConnection(ctx, cfg)
	if e != nil {
		e.Close()
	}
	if err == nil {
		t.Fatal("a canceled handshake against a dead host answered success")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("cancellation did not bound the handshake: took %s", elapsed)
	}
}
