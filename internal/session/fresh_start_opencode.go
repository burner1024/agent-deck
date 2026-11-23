package session

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/shellwords"
)

const openCodeTurnDir = "opencode-turn"

type freshStartOpenCodeTurn struct {
	TurnID string `json:"turn_id"`
}

// Turn identity survives status aggregation, reconnects and daemon restarts.
// Stream identifies the subscription that owns the empty-history proof.
type openCodeTurnObservation struct {
	At          time.Time `json:"at"`
	TurnID      string    `json:"turn_id"`
	CompletedID string    `json:"completed_id"`
	Running     bool      `json:"running"`
	Ran         bool      `json:"ran"`
	Ready       bool      `json:"ready"`
	Stream      string    `json:"stream"`
	ObservedAt  time.Time `json:"observed_at"`
}

func readOpenCodeTurn(id string) openCodeTurnObservation {
	var obs openCodeTurnObservation
	if path, err := runtimeTokenPath(openCodeTurnDir, id, ".json"); err == nil {
		if data, err := os.ReadFile(path); err == nil {
			if json.Unmarshal(data, &obs) != nil {
				return openCodeTurnObservation{}
			}
		}
	}
	return obs
}

// Sync and Stop hold the watcher lock and revoke proof before cancellation,
// so cancelled streams never need a late write to the runtime directory.
func revokeOpenCodeBootProof(id string) {
	obs := readOpenCodeTurn(id)
	if obs.Ready {
		obs.Ready = false
		writeTokenFile(openCodeTurnDir, id, ".json", obs)
	}
}

func openCodeFreshStartObservable(i *Instance, command string) bool {
	if i.OpenCodeSessionID != "" || i.notLocal() || i.hasEffectiveWrapper() {
		return false
	}
	if opts := i.GetOpenCodeOptions(); opts != nil && (opts.SessionMode == "resume" || opts.SessionMode == "continue") {
		return false
	}
	if cfg, err := LoadUserConfig(); err != nil || (cfg != nil && cfg.OpenCode.DisableSSEStatus) {
		return false
	}
	words, ok := shellwords.Split(command)
	if !ok || len(words) != 1 {
		return false
	}
	base := filepath.Base(words[0])
	return base == "opencode" || base == "opencode2"
}

// Only the current, uncancelled subscription can record observations. A late
// disconnect from a replaced stream must not erase its successor's proof.
func (w *OpenCodeSSEWatcher) observeOpenCodeTurn(ctx context.Context, id, stream string, begin bool, update func(*openCodeTurnObservation)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	obs := readOpenCodeTurn(id)
	if begin {
		if tok, ok := readFreshStartToken(id); ok && tok.OpenCode != nil && !obs.At.Equal(tok.At) {
			obs = openCodeTurnObservation{At: tok.At, TurnID: tok.OpenCode.TurnID}
		}
	}
	if !begin && obs.Stream != stream {
		return
	}
	if update == nil && !begin {
		tok, ok := readFreshStartToken(id)
		if (!ok || tok.OpenCode == nil) && (!obs.Running || time.Since(obs.ObservedAt) < opencodeSSEFreshnessWindow/2) {
			return
		}
	}
	if begin {
		obs.Stream = stream
		obs.Ready = false
	}
	if update != nil {
		previous := obs
		update(&obs)
		if !begin && obs == previous {
			return
		}
	}
	obs.ObservedAt = time.Now()
	writeTokenFile(openCodeTurnDir, id, ".json", obs)
}

// No status snapshot proves that a turn did not finish before subscription.
// Read message history while subscribed; failure leaves boot undecided.
func (w *OpenCodeSSEWatcher) seedOpenCodeFreshStart(ctx context.Context, id, stream string, src openCodeEventSource) {
	tok, ok := readFreshStartToken(id)
	if !ok || tok.OpenCode == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	sessions := []string{src.sessionID}
	if src.sessionID == "" {
		// V1 defaults to a limited list. Filter at the server and refuse a
		// full page, whose unseen tail could contain a completed conversation.
		const limit = 100
		path := "/session?roots=true&limit=" + strconv.Itoa(limit) + "&start=" + strconv.FormatInt(tok.At.UnixMilli(), 10)
		data, ok := w.openCodeFreshSnapshot(ctx, src, path)
		if !ok {
			return
		}
		var roots []openCodeHTTPSessionMetadata
		if json.Unmarshal(data, &roots) != nil || roots == nil || len(roots) >= limit {
			return
		}
		sessions = nil
		for _, root := range roots {
			if root.Time.Created == 0 {
				return
			}
			if root.ParentID == "" && root.Time.Created >= tok.At.UnixMilli() {
				sessions = append(sessions, root.ID)
			}
		}
	}
	for _, sessionID := range sessions {
		if sessionID == "" {
			return
		}
		path := "/session/" + url.PathEscape(sessionID) + "/message?limit=1&order=desc"
		hasMore := false
		if src.sessionID != "" {
			path = "/api" + path
		}
		data, ok := w.openCodeFreshSnapshot(ctx, src, path)
		if !ok {
			return
		}
		if src.sessionID != "" {
			var page struct {
				Data   json.RawMessage `json:"data"`
				Cursor struct {
					Next json.RawMessage `json:"next"`
				} `json:"cursor"`
			}
			if json.Unmarshal(data, &page) != nil {
				return
			}
			hasMore = len(page.Cursor.Next) > 0 && string(page.Cursor.Next) != "null" && string(page.Cursor.Next) != `""`
			data = page.Data
		}
		var messages []struct {
			ID   string `json:"id"`
			Info struct {
				ID string `json:"id"`
			} `json:"info"`
		}
		if json.Unmarshal(data, &messages) != nil || messages == nil {
			return
		}
		if len(messages) > 0 {
			turnID := messages[0].ID
			if turnID == "" {
				turnID = messages[0].Info.ID
			}
			w.observeOpenCodeTurn(ctx, id, stream, false, func(obs *openCodeTurnObservation) {
				obs.Ran = true
				if turnID != "" {
					obs.TurnID = turnID
					if !obs.Running {
						obs.CompletedID = turnID
					}
				}
			})
			return
		}
		if hasMore {
			return
		}
	}
	w.observeOpenCodeTurn(ctx, id, stream, false, func(obs *openCodeTurnObservation) { obs.Ready = true })
}

func (w *OpenCodeSSEWatcher) openCodeFreshSnapshot(ctx context.Context, src openCodeEventSource, path string) ([]byte, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.base+path, nil)
	if err != nil {
		return nil, false
	}
	src.authorize(req)
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return data, err == nil && json.Valid(data)
}
