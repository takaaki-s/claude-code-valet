package session

import "encoding/json"

// migrateSessionJSON applies schema migrations to raw session JSON in place and
// reports whether anything changed. It is idempotent.
//
//   - v1 → v2: "name" → "description", and "description_locked" = true (the
//     historical value was chosen by the user, so lock it).
//   - v2 → v3: "claude_session_id" / "claude_session_started" → their
//     agent-agnostic names, and backfill "agent_kind" with "claude" — records
//     predating the agent split are by definition Claude Code sessions.
func migrateSessionJSON(raw []byte) ([]byte, bool, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false, err
	}

	changed := false

	// v1 → v2 -----------------------------------------------------------
	if rawName, ok := m["name"]; ok {
		if name, isString := rawName.(string); isString && name != "" {
			if desc, _ := m["description"].(string); desc == "" {
				m["description"] = name
				m["description_locked"] = true
			}
		}
		delete(m, "name")
		changed = true
	}

	// v2 → v3. A record that already carries a non-empty agent_kind is left
	// alone, for idempotency and for agents added later.
	if k, _ := m["agent_kind"].(string); k == "" {
		m["agent_kind"] = "claude"
		changed = true
	}
	// Do not clobber a value the new field already carries. That only happens
	// after a hand-edit mid-migration, and keeping the newer field is safer.
	if rawCC, ok := m["claude_session_id"]; ok {
		if id, isString := rawCC.(string); isString && id != "" {
			if existing, _ := m["agent_session_id"].(string); existing == "" {
				m["agent_session_id"] = id
			}
		}
		delete(m, "claude_session_id")
		changed = true
	}
	if rawStarted, ok := m["claude_session_started"]; ok {
		if started, isBool := rawStarted.(bool); isBool {
			if _, existing := m["agent_session_started"].(bool); !existing {
				m["agent_session_started"] = started
			}
		}
		delete(m, "claude_session_started")
		changed = true
	}

	if !changed {
		return raw, false, nil
	}

	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
