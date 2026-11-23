package statedb

import (
	"database/sql"
	"fmt"
)

// LoadInstanceIDByTmuxSession returns the owning instance ID, or an empty
// string when no row names tmuxSession. It reads only the ID column.
func (s *StateDB) LoadInstanceIDByTmuxSession(tmuxSession string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT id FROM instances WHERE tmux_session = ? LIMIT 1`, tmuxSession).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("loading instance id by tmux session: %w", err)
	}
	return id, nil
}
