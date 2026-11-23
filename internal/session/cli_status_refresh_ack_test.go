package session

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRefreshInstancesForCLIStatus_OpenCodeValidationBound(t *testing.T) {
	skipIfNoTmuxBinary(t)
	project := t.TempDir()
	t.Setenv("HOME", project)
	var mu sync.Mutex
	active, peak := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		peak = max(peak, active)
		mu.Unlock()
		defer func() {
			mu.Lock()
			active--
			mu.Unlock()
		}()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	seed := NewInstanceWithTool("opencode-stall", project, "opencode")
	seed.tmuxSession.RunCommandAsInitialProcess = true
	if err := seed.tmuxSession.Start(fmt.Sprintf("sh -c 'exec sleep 300' opencode --port %d", port)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = seed.tmuxSession.Kill() })
	instances := make([]*Instance, 8)
	for n := range instances {
		inst := NewInstanceWithTool(fmt.Sprintf("opencode-stall-%d", n), project, "opencode")
		// Independent restored acknowledgement state over the same test pane.
		inst.tmuxSession.Name = seed.tmuxSession.Name
		inst.tmuxSession.SocketName = seed.tmuxSession.SocketName
		inst.tmuxSession.Acknowledge()
		inst.ackedOpenCodeUpdated = 200
		instances[n] = inst
	}
	started := time.Now()
	RefreshInstancesForCLIStatus(instances)
	if elapsed := time.Since(started); elapsed > 5*openCodeAckQueryTimeout/2 {
		t.Errorf("validation and warm-up took %s, want less than 2.5 request timeouts", elapsed)
	}
	mu.Lock()
	maxActive := peak
	mu.Unlock()
	if maxActive < 2 || maxActive > 4 {
		t.Errorf("peak concurrent requests = %d, want 2 through 4", maxActive)
	}
	for n, inst := range instances {
		if inst.tmuxSession.IsAcknowledged() {
			t.Errorf("row %d remains acknowledged without a verified turn", n)
		}
	}
}
