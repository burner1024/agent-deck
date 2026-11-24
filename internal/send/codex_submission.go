package send

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Codex's own acknowledgement that it took a message (issue #2424).
//
// Codex 0.15x draws its composer and every user message in the transcript the
// same way: "›" at column 0, wrapped and multi-line continuations indented two
// columns. A cell followed by transcript output is a submitted message; the
// last cell without such output is the composer. When Codex takes a message
// it moves it out of the composer into a transcript cell and, while the turn
// runs, shows its live status row
// ("• Working (6s • esc to interrupt)") above the composer. A lane that was
// already working shows that row too, which is why the transcript cell and
// the live row are each read together with "the composer no longer holds
// this send's body" and never on their own.
//
// A message Codex parks in its "• Queued follow-up inputs" block ("  ↳ ")
// has not entered a turn yet and is not read as submitted here.

const codexPromptGlyph = "›"

// codexStartupPending recognizes the footer Codex draws after accepting Enter
// during startup. The message waits in the composer until initialization ends.
// Only the final nonblank row counts; transcript text cannot acknowledge it.
func codexStartupPending(content string) bool {
	rows := strings.Split(strings.TrimSpace(content), "\n")
	if strings.Join(strings.Fields(rows[len(rows)-1]), " ") != "Waiting for startup \u00b7 esc cancel" {
		return false
	}
	cells := codexCells(content)
	return len(cells) > 0 && !cells[len(cells)-1].followedByTranscript
}

// codexCell is one column-0 "›" cell of a Codex frame.
type codexCell struct {
	// text is the cell's body, glyph removed and whitespace-collapsed.
	text string
	// followedByTranscript reports a column-0 transcript row (a "• " agent
	// or status row, a "─ Worked for … ─" rule) between this cell and the
	// next one. Codex draws one between its newest user message and the
	// composer; a picker row drawn right under the composer has none.
	followedByTranscript bool
}

// codexCells splits an ANSI-stripped Codex frame into its column-0 "›" cells,
// top to bottom. A cell continues through rows indented two columns, and
// through blank rows when the next non-blank row is still indented (a blank
// line inside a message). The composer cell therefore also takes in the
// model/context footer below it, which never carries message text.
func codexCells(content string) []codexCell {
	rows := strings.Split(content, "\n")
	var cells []codexCell
	for i := 0; i < len(rows); i++ {
		if !strings.HasPrefix(rows[i], codexPromptGlyph) {
			if len(cells) > 0 && strings.TrimSpace(rows[i]) != "" && !startsIndented(rows[i]) {
				cells[len(cells)-1].followedByTranscript = true
			}
			continue
		}
		body := []string{strings.TrimPrefix(rows[i], codexPromptGlyph)}
		j := i + 1
		for j < len(rows) {
			if startsIndented(rows[j]) && strings.TrimSpace(rows[j]) != "" {
				body = append(body, rows[j])
				j++
				continue
			}
			if strings.TrimSpace(rows[j]) == "" {
				next := j + 1
				for next < len(rows) && strings.TrimSpace(rows[next]) == "" {
					next++
				}
				if next < len(rows) && startsIndented(rows[next]) {
					j = next
					continue
				}
			}
			break
		}
		cells = append(cells, codexCell{text: collapse(strings.Join(body, " "))})
		i = j - 1
	}
	return cells
}

func startsIndented(row string) bool {
	return strings.HasPrefix(row, "  ")
}

// codexTranscriptCopies counts cells that begin with token and are followed
// by transcript rows. The last cell also counts when the composer is absent
// from the capture but a reply or live turn row follows the user message.
func codexTranscriptCopies(content, token string) int {
	if token == "" {
		return 0
	}
	cells := codexCells(content)
	n := 0
	for i := 0; i < len(cells); i++ {
		if cells[i].followedByTranscript && strings.HasPrefix(cells[i].text, token) {
			n++
		}
	}
	return n
}

// codexQueuedHolds reports whether Codex's queued follow-up inputs list
// ("  ↳ <message>" rows with four-column continuations) holds token.
func codexQueuedHolds(content, token string) bool {
	rows := strings.Split(content, "\n")
	for i, row := range rows {
		if !strings.HasPrefix(row, "  ↳ ") {
			continue
		}
		entry := []string{strings.TrimPrefix(row, "  ↳ ")}
		for j := i + 1; j < len(rows) && strings.HasPrefix(rows[j], "    ") && strings.TrimSpace(rows[j]) != ""; j++ {
			entry = append(entry, rows[j])
		}
		if strings.HasPrefix(collapse(strings.Join(entry, " ")), token) {
			return true
		}
	}
	return false
}

// codexTookMessage reports whether an ANSI-stripped Codex frame shows Codex
// took this send's message. A new copy of the body must be on screen (copies
// above baselineCopies, the whole-frame token counts after and before the
// send) and the composer must not hold it. A last cell followed by transcript
// rows is a submitted user turn, not a composer. Then either a new
// transcript cell carries it (more than baselineCells), or it is not parked
// in the queued list and Codex's live status row shows a turn running.
func codexTookMessage(content, token string, copies, baselineCopies, baselineCells int) bool {
	if token == "" || copies <= baselineCopies {
		return false
	}
	cells := codexCells(content)
	if len(cells) == 0 {
		return false
	}
	last := cells[len(cells)-1]
	if !last.followedByTranscript && (strings.Contains(last.text, token) || CountPasteMarkers(last.text) > 0) {
		return false
	}
	if codexTranscriptCopies(content, token) > baselineCells {
		return true
	}
	return !codexQueuedHolds(content, token) && tmux.CodexTurnRunning(content)
}
