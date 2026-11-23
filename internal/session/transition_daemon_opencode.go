package session

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
