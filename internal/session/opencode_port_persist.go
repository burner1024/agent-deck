package session

import (
	"encoding/json"
	"fmt"
)

// The event server belongs to the child, so its port must survive a reload
// by another process. Zero is explicit to clear a previous launch's port.
func writeOpenCodePortToToolData(data json.RawMessage, port int) (json.RawMessage, error) {
	fields := make(map[string]json.RawMessage)
	if len(data) > 0 {
		if err := json.Unmarshal(data, &fields); err != nil {
			return nil, fmt.Errorf("decode OpenCode port metadata: %w", err)
		}
	}
	value, err := json.Marshal(port)
	if err != nil {
		return nil, fmt.Errorf("encode OpenCode port: %w", err)
	}
	fields["opencode_port"] = value
	data, err = json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode OpenCode port metadata: %w", err)
	}
	return data, nil
}

func readOpenCodePortFromToolData(data json.RawMessage) int {
	var fields struct {
		Port int `json:"opencode_port"`
	}
	if json.Unmarshal(data, &fields) != nil || fields.Port <= 0 || fields.Port > 65535 {
		return 0
	}
	return fields.Port
}
