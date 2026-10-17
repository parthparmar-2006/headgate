package limiter

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func startSilentServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var mu sync.Mutex
	conns := []net.Conn{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			conn.Close()
		}
	})
	return listener.Addr().String()
}

func closedPortAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()
	return addr
}

func timeAllow(t *testing.T, client redis.Scripter, budget time.Duration) (time.Duration, error) {
	t.Helper()
	l := NewRedisFixedWindow(client)
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	start := time.Now()
	_, err := l.Allow(ctx, "silent", Rule{Limit: 10, Window: time.Second})
	return time.Since(start), err
}

func TestRedisClientGivesUpWithinBudgetWhenServerIsSilent(t *testing.T) {
	client := NewRedisClient(startSilentServer(t), 0)
	defer client.Close()

	elapsed, err := timeAllow(t, client, 50*time.Millisecond)

	if err == nil {
		t.Fatalf("expected an error from a silent server")
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("took %v with a 50ms budget", elapsed)
	}
}

func TestRedisClientFailsFastWhenPortIsClosed(t *testing.T) {
	client := NewRedisClient(closedPortAddr(t), 0)
	defer client.Close()

	elapsed, err := timeAllow(t, client, 50*time.Millisecond)

	if err == nil {
		t.Fatalf("expected an error from a closed port")
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("took %v with a 50ms budget", elapsed)
	}
}

func TestSpikeDefaultClientIgnoresBudget(t *testing.T) {
	requireExperiments(t)
	client := redis.NewClient(&redis.Options{Addr: startSilentServer(t)})
	defer client.Close()

	elapsed, err := timeAllow(t, client, 50*time.Millisecond)

	t.Logf("default go-redis options, 50ms budget, silent server: returned after %v with error %v", elapsed, err)
}
