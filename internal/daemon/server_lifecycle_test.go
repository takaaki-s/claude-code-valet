package daemon

import (
	"io"
	"log"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/takaaki-s/jind-ai/internal/testutil"
)

// These tests exercise Start's accept loop concurrently with Stop — the one
// pairing the rest of the package never produces, which is why the
// Server.listener data race survived `go test -race ./internal/daemon/` and
// only surfaced under `go test -tags e2e -race ./test/e2e/`. Keeping the
// pairing here means the plain `-race ./...` run catches a regression.
//
// A bare &Server{} is enough: Start touches only socketPath and the listener
// state, and the probe connections below are closed without sending a
// request, so handleConnection returns at the io.EOF decode without reaching
// s.manager.

// startedServer binds a server on a temp socket, runs Start in a goroutine,
// and returns the server plus a channel carrying Start's return value.
func startedServer(t *testing.T) (*Server, <-chan error) {
	t.Helper()

	// A pre-fix Start can spin hot on a closed listener ("Accept error" every
	// iteration) when it misses the stop sentinel. Discarding log output keeps
	// a failing run from flooding the terminal.
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	s := &Server{socketPath: testutil.SocketPath(t, "d.sock")}

	errCh := make(chan error, 1)
	go func() { errCh <- s.Start() }()
	waitListening(t, s.socketPath)
	return s, errCh
}

// waitListening dials the socket until it connects, proving Start got past
// net.Listen.
//
// Deliberately not a wait on s.listener becoming non-nil: a successful dial
// can land in the window between net.Listen returning and Start publishing
// the field, so a Stop that follows exercises the order-independence of the
// two — the narrower wait would hide it.
func waitListening(t *testing.T, sockPath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("socket %s never became connectable", sockPath)
}

// waitStartReturned fails the test if Start does not unwind promptly after a
// Stop. Besides the data race, a missed stop sentinel leaves the accept loop
// spinning on the closed listener instead of returning.
func waitStartReturned(t *testing.T, errCh <-chan error) {
	t.Helper()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Start returned %v, want nil after Stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return within 5s of Stop (accept loop missed the stop sentinel)")
	}
}

// TestServer_StopWhileAccepting is the direct regression test for the
// Server.listener race: the accept loop reads the field to decide whether an
// Accept error means "we were stopped", while Stop writes it from another
// goroutine.
func TestServer_StopWhileAccepting(t *testing.T) {
	s, errCh := startedServer(t)

	s.Stop()

	waitStartReturned(t, errCh)
}

// TestServer_StopIsIdempotentUnderConcurrency covers the second half of the
// hazard: Start installs a signal handler goroutine that calls Stop, and
// handleStop / test cleanups call it explicitly. Both can fire at once, so
// Stop must tolerate concurrent and repeated calls without double-closing the
// listener or racing on its own state.
func TestServer_StopIsIdempotentUnderConcurrency(t *testing.T) {
	s, errCh := startedServer(t)

	const callers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s.Stop()
		}()
	}
	close(start) // release all callers at once
	wg.Wait()

	waitStartReturned(t, errCh)

	// A second round after everything has settled must still be a no-op.
	s.Stop()
}

// TestServer_StopRemovesSocket pins the observable postcondition, so a future
// refactor of the stop path cannot quietly drop the socket cleanup.
func TestServer_StopRemovesSocket(t *testing.T) {
	s, errCh := startedServer(t)

	s.Stop()
	waitStartReturned(t, errCh)

	if _, err := os.Stat(s.socketPath); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%s) err = %v, want not-exist after Stop", s.socketPath, err)
	}
}
