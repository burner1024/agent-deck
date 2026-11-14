package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/sandboxguard"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2683: Claude Code's agent-view supervisor and its pre-spawned
// bg-spare workers keep the environment of the pane that first started them,
// AGENTDECK_INSTANCE_ID included. A conversation later backgrounded from a
// DIFFERENT pane runs in that worker, so its hooks name the wrong instance.
// A non-bg record parking the worker's job identifies its owning tmux pane.
// Route hooks to that pane's instance; drop hooks without a known owner.

// maxBgWorkerAncestorHops bounds the walk from the hook process up to the
// Claude process that fired it (the hook may run under an intermediate shell).
const maxBgWorkerAncestorHops = 3

// hookClaudeInstance returns the instance ID and profile to credit, or an
// empty ID when a background worker's job has no known parking instance.
//
// The nearest ancestor with a ~/.claude/sessions/<pid>.json record that names
// a kind is authoritative: kind "bg" requires a parking owner, any other
// kind credits the inherited ID, even when CLAUDE_JOB_DIR is set.
// CLAUDE_JOB_DIR alone can leak into a pane through its tmux server's
// environment, and treating it as proof would drop every hook on that server.
// It is only consulted when no such record is found (e.g. /proc is missing,
// or the worker wrote none).
//
// The parking owner can differ from the inherited instance and profile.
// Foreground hooks return an empty profile to keep normal profile resolution.
func hookClaudeInstance(instanceID string) (string, string) {
	claudeDir := claudeConfigDirForHook()
	jobID := ""
	if rec, ok := nearestClaudeAncestorRecord(claudeDir, os.Getppid()); ok {
		if rec.Kind != "bg" {
			return instanceID, ""
		}
		jobID = rec.JobID
	} else {
		jobDir := strings.TrimSpace(os.Getenv("CLAUDE_JOB_DIR"))
		if jobDir == "" {
			return instanceID, ""
		}
		jobID = filepath.Base(jobDir)
	}
	return jobParkingInstance(claudeDir, jobID)
}

// claudeSessionRecord is the part of a ~/.claude/sessions/<pid>.json record
// the hook handler reads.
type claudeSessionRecord struct {
	Kind        string `json:"kind"`
	JobID       string `json:"jobId"`
	ParkedJobID string `json:"parkedJobId"`
	Tmux        string `json:"tmux"`
}

func claudeConfigDirForHook() string {
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); dir != "" {
		return dir
	}
	dir, err := sandboxguard.HomeJoin("Claude config dir (~/.claude)", ".claude")
	if err != nil {
		return ""
	}
	return dir
}

// nearestClaudeAncestorRecord walks up from pid (at most
// maxBgWorkerAncestorHops processes) and returns the first
// ~/.claude/sessions/<pid>.json record that names a kind.
func nearestClaudeAncestorRecord(claudeDir string, pid int) (claudeSessionRecord, bool) {
	if claudeDir == "" {
		return claudeSessionRecord{}, false
	}
	for hop := 0; hop < maxBgWorkerAncestorHops && pid > 1; hop++ {
		data, err := os.ReadFile(filepath.Join(claudeDir, "sessions", strconv.Itoa(pid)+".json"))
		if err == nil {
			var rec claudeSessionRecord
			if json.Unmarshal(data, &rec) == nil && strings.TrimSpace(rec.Kind) != "" {
				rec.Kind = strings.TrimSpace(rec.Kind)
				rec.JobID = strings.TrimSpace(rec.JobID)
				return rec, true
			}
			return claudeSessionRecord{}, false
		}
		pid = parentPIDOf(pid)
	}
	return claudeSessionRecord{}, false
}

// jobParkingInstance resolves the instance whose tmux session parks jobID.
// The tmux field reads "<session>:@<window>.%<pane>". No state database is
// opened unless a non-bg record parks the job. All existing profiles are
// searched read-only because the worker's inherited profile can be stale.
func jobParkingInstance(claudeDir, jobID string) (string, string) {
	if claudeDir == "" || jobID == "" || jobID == "." || jobID == string(filepath.Separator) {
		return "", ""
	}
	entries, err := os.ReadDir(filepath.Join(claudeDir, "sessions"))
	if err != nil {
		return "", ""
	}
	var parkedIn []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(claudeDir, "sessions", e.Name()))
		if err != nil {
			continue
		}
		var rec claudeSessionRecord
		if json.Unmarshal(data, &rec) != nil || strings.TrimSpace(rec.Kind) == "" || strings.TrimSpace(rec.Kind) == "bg" ||
			strings.TrimSpace(rec.ParkedJobID) != jobID {
			continue
		}
		if name, _, ok := strings.Cut(strings.TrimSpace(rec.Tmux), ":"); ok && name != "" {
			parkedIn = append(parkedIn, name)
		}
	}
	if len(parkedIn) == 0 {
		return "", ""
	}
	effectiveProfile, err := session.ResolveProfileForStorage("")
	if err != nil {
		return "", ""
	}
	profiles := []string{effectiveProfile}
	if others, err := session.ListProfiles(); err == nil {
		for _, profile := range others {
			if profile != effectiveProfile {
				profiles = append(profiles, profile)
			}
		}
	}
	for _, profile := range profiles {
		id := parkingInstanceInProfile(profile, parkedIn)
		if id != "" && validInstanceID.MatchString(id) && !strings.Contains(id, "..") {
			return id, profile
		}
	}
	return "", ""
}

func parkingInstanceInProfile(profile string, tmuxNames []string) string {
	storage, err := session.NewLiveReadOnlyStorageWithProfile(profile)
	if err != nil {
		return ""
	}
	defer storage.Close() // Read-only lookup; there are no writes to flush.
	for _, name := range tmuxNames {
		if id, err := storage.GetDB().LoadInstanceIDByTmuxSession(name); err == nil && id != "" {
			return id
		}
	}
	return ""
}

// scrubClaudeJobDirForCommand removes CLAUDE_JOB_DIR from this process's
// environment before any tmux command can run (#2683), so a tmux server that
// agent-deck starts from inside a Claude background job never carries it into
// its panes. The hook receivers keep it: it is one of their bg-worker signals,
// and every tmux exec it makes goes through the filtered tmux spawn env.
func scrubClaudeJobDirForCommand(args []string) {
	if len(args) > 0 && (args[0] == "hook-handler" ||
		(len(args) > 1 && args[0] == "trace" && args[1] == "hook")) {
		return
	}
	_ = os.Unsetenv("CLAUDE_JOB_DIR")
}

// parentPIDOf returns pid's parent via /proc, or 0 where /proc is absent
// (macOS: only the direct parent is checked there).
func parentPIDOf(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// Format: pid (comm) state ppid ...; comm may contain spaces/parens.
	s := string(data)
	idx := strings.LastIndexByte(s, ')')
	if idx < 0 {
		return 0
	}
	fields := strings.Fields(s[idx+1:])
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

// logBgWorkerHookDropOnce records one session-id lifecycle reject per
// (instance, worker session), so a busy worker does not flood the log.
func logBgWorkerHookDropOnce(instanceID, sessionID, event string) {
	hooksDir := getHooksDir()
	markerPath := filepath.Join(hooksDir, filepath.Base(instanceID)+".bg-worker-dropped")
	if existing, err := os.ReadFile(markerPath); err == nil && strings.TrimSpace(string(existing)) == sessionID {
		return
	}
	_ = session.WriteSessionIDLifecycleEvent(session.SessionIDLifecycleEvent{
		InstanceID: instanceID,
		Tool:       "claude",
		Action:     "reject",
		Source:     "hook_payload",
		Candidate:  sessionID,
		Reason:     "hook_from_background_worker",
		HookEvent:  event,
	})
	if err := os.MkdirAll(hooksDir, 0o700); err == nil {
		_ = os.WriteFile(markerPath, []byte(sessionID), 0o600)
	}
}
