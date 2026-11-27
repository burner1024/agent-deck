package main

import (
	"fmt"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// openCodeLaunchIdentityWait bounds how long launch waits for the reader of an
// OpenCode child's own event stream to name the child's session. The session is
// created when the initial prompt is submitted, which has happened by then.
const openCodeLaunchIdentityWait = 10 * time.Second

// persistLiveOpenCodeIdentity writes the session an OpenCode child's own event
// stream named as the instance's OpenCode identity, with the targeted binding
// write that touches no other column. It waits up to wait for the binding.
//
// The stream reader ends with the launching process, and no other process reads
// that stream for a child without a TUI watching it; the directory listing the
// others could read names every sibling's session too. Without this write the
// row stays unbound.
func persistLiveOpenCodeIdentity(storage *session.Storage, inst *session.Instance, wait time.Duration) {
	if inst == nil || inst.Tool != "opencode" || storage == nil || storage.GetDB() == nil {
		return
	}
	id, detectedAt := inst.AwaitOpenCodeStreamBinding(wait)
	if id == "" {
		return
	}
	if err := storage.GetDB().WriteOpenCodeSessionBinding(inst.ID, id, detectedAt); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not persist OpenCode session identity: %v\n", err)
	}
}
