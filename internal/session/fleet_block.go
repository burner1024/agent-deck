package session

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Fleet-block change detection (issue #2469, design principle 5). The parent's
// UserPromptSubmit hook injects a children snapshot; it used to repeat the
// same text on every prompt. The hook now records a fingerprint of what it
// last injected and skips an unchanged snapshot.

func fleetBlockDir() string {
	return runtimeDirOrTemp("fleet-block")
}

func fleetBlockPath(parentID string) string {
	return filepath.Join(fleetBlockDir(), sanitizeInboxName(parentID)+".fp")
}

// FleetBlockUnchanged reports whether summary equals the last snapshot
// injected for this parent, and records summary as the latest either way.
func FleetBlockUnchanged(parentID, summary string) bool {
	return fleetBlockUnchanged(parentID, summary, true)
}

func fleetBlockUnchanged(parentID, summary string, record bool) bool {
	if strings.TrimSpace(parentID) == "" {
		return false
	}
	sum := sha256.Sum256([]byte(summary))
	fp := hex.EncodeToString(sum[:16])
	path := fleetBlockPath(parentID)
	if prev, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(prev)) == fp { // #nosec G304 -- sanitized id under the data dir
		return true
	}
	if record {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			_ = os.WriteFile(path, []byte(fp+"\n"), 0o600) // Best effort: a lost marker permits another notice.
		}
	}
	return false
}

func fleetCompletionBlock(parentID string, e CompletionLedgerEntry) (string, string) {
	return parentID + ".completion." + e.ChildID,
		e.FinishedAt.Format(time.RFC3339Nano) + "/" + e.TurnUUID + "/" + e.Status + "/" + e.Summary
}

// FleetCompletionNotified reports whether the parent already received this
// completion, read its result, or saw its fleet bullet. Reading this state
// does not acknowledge a full inbox result when only a summary will be shown.
func FleetCompletionNotified(parentID string, e CompletionLedgerEntry) bool {
	if e.TurnUUID != "" {
		ev := completionLedgerEvent(e)
		ev.TurnUUID = e.TurnUUID
		ev.LastOutputHash = "turn:" + e.TurnUUID
		if turnAlreadyConsumed(parentID, TurnFingerprint(ev)) || loadResultReads(parentID).covers(ev) {
			return true
		}
	}
	key, value := fleetCompletionBlock(parentID, e)
	return fleetBlockUnchanged(key, value, false)
}

// NoteFleetCompletion remembers a displayed summary separately from full
// result delivery. Overflow bullets must not be recorded until displayed.
func NoteFleetCompletion(parentID string, e CompletionLedgerEntry) {
	key, value := fleetCompletionBlock(parentID, e)
	FleetBlockUnchanged(key, value)
}
