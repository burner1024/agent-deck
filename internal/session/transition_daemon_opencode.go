package session

import (
	"fmt"
	"time"
)

const openCodeTerminalHold = 4 * time.Second

type openCodePollTurn struct {
	running bool
	signal  string
}

// A pane can prove separate runs even when SSE status is disabled. Keep one
// identity for each observed running interval so boot cannot consume its end.
func (d *TransitionDaemon) observeOpenCodePollTurns(profile string, byID map[string]*Instance, statuses map[string]string, now time.Time) {
	for id, status := range statuses {
		inst := byID[id]
		if inst == nil || inst.Tool != "opencode" || inst.notLocal() || inst.hasEffectiveWrapper() {
			continue
		}
		if d.openCodePollTurns == nil {
			d.openCodePollTurns = make(map[string]map[string]openCodePollTurn)
		}
		if d.openCodePollTurns[profile] == nil {
			d.openCodePollTurns[profile] = make(map[string]openCodePollTurn)
		}
		turn := d.openCodePollTurns[profile][id]
		running := normalizeStatusString(status) == string(StatusRunning)
		if running && !turn.running {
			turn.signal = fmt.Sprintf("opencode-run:%s:%d", id, now.UnixNano())
		}
		turn.running = running
		d.openCodePollTurns[profile][id] = turn
	}
	for id := range d.openCodePollTurns[profile] {
		if _, ok := statuses[id]; !ok {
			delete(d.openCodePollTurns[profile], id)
		}
	}
}

func (d *TransitionDaemon) openCodePollSignal(profile string, inst *Instance, signal string) string {
	if inst.Tool != "opencode" || inst.notLocal() || inst.hasEffectiveWrapper() {
		return signal
	}
	if cfg, err := LoadUserConfig(); err == nil && cfg != nil && cfg.OpenCode.DisableSSEStatus {
		// A disabled stream's old completion must not hide subsequent turns.
		signal = ""
	}
	if signal == "" {
		return d.openCodePollTurns[profile][inst.ID].signal
	}
	return signal
}

// Bound disagreement between a terminal pane and a running observation.
// Stream liveness cannot extend this deadline. A running poll clears it.
func (d *TransitionDaemon) holdOpenCodeTerminal(profile, id string, now time.Time) bool {
	obs := readOpenCodeTurn(id)
	if !obs.Running || now.Sub(obs.ObservedAt) >= opencodeSSEFreshnessWindow {
		delete(d.openCodeHolds[profile], id)
		return false
	}
	if d.openCodeHolds == nil {
		d.openCodeHolds = make(map[string]map[string]time.Time)
	}
	if d.openCodeHolds[profile] == nil {
		d.openCodeHolds[profile] = make(map[string]time.Time)
	}
	if at, ok := d.openCodeHolds[profile][id]; ok {
		return now.Sub(at) < openCodeTerminalHold
	}
	d.openCodeHolds[profile][id] = now
	return true
}

// Each resolved profile gets its own target set. Syncing one profile must
// never cancel another profile's streams. All calls run on the polling loop.
func (d *TransitionDaemon) syncOpenCodeWatcher(profile string, instances []*Instance) {
	if d.openCodeWatchers == nil {
		return
	}
	var targets []SSETarget
	config, err := LoadUserConfig()
	if err == nil && (config == nil || !config.OpenCode.DisableSSEStatus) {
		for _, inst := range instances {
			if inst.Tool != "opencode" || inst.ParentSessionID == "" || inst.notLocal() || inst.hasEffectiveWrapper() || !isLiveSessionStatus(inst.Status) {
				continue
			}
			if d.turnLiveCheck != nil && !d.turnLiveCheck(inst) {
				continue
			}
			if target, ok := inst.OpenCodeSSETarget(); ok {
				targets = append(targets, target)
			}
		}
	}
	watcher := d.openCodeWatchers[profile]
	if len(targets) == 0 {
		if watcher != nil {
			watcher.Stop()
			delete(d.openCodeWatchers, profile)
		}
		return
	}
	if watcher == nil {
		watcher = NewOpenCodeSSEWatcher(nil)
		d.openCodeWatchers[profile] = watcher
	}
	watcher.Sync(targets)
	for _, inst := range instances {
		if status := watcher.GetStatus(inst.ID); status != nil {
			inst.UpdateOpenCodeSSEStatus(status.Status, status.UpdatedAt)
		}
	}
}

func (d *TransitionDaemon) pruneOpenCodeWatchers(profiles []string) {
	want := make(map[string]bool, len(profiles))
	for _, profile := range profiles {
		want[profile] = true
	}
	for profile, watcher := range d.openCodeWatchers {
		if !want[profile] {
			watcher.Stop()
			delete(d.openCodeWatchers, profile)
		}
	}
}
