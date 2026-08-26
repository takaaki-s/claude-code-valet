package session

import (
	"encoding/json"
	"testing"
)

func TestMigrateSessionJSON(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantChanged    bool
		wantErr        bool
		wantDesc       string
		wantLocked     bool
		wantNameAbsent bool
		wantAgentKind  string
		wantAgentID    string
		wantAgentStart bool
		wantConfirmed  bool
		wantCCAbsent   bool // claude_session_id / claude_session_started must be gone
	}{
		{
			name:           "legacy v1 schema (name only) migrates description + backfills agent_kind",
			input:          `{"id":"abc","name":"my-session","work_dir":"/tmp/x"}`,
			wantChanged:    true,
			wantDesc:       "my-session",
			wantLocked:     true,
			wantNameAbsent: true,
			wantAgentKind:  "claude",
			wantCCAbsent:   true,
		},
		{
			name:           "legacy v1 schema with empty name only drops the field but still backfills agent_kind",
			input:          `{"id":"abc","name":"","work_dir":"/tmp/x"}`,
			wantChanged:    true,
			wantDesc:       "",
			wantLocked:     false,
			wantNameAbsent: true,
			wantAgentKind:  "claude",
			wantCCAbsent:   true,
		},
		{
			name:          "v2 schema (description present, no agent_kind) gets agent_kind backfilled",
			input:         `{"id":"abc","description":"my-session","description_locked":true,"work_dir":"/tmp/x"}`,
			wantChanged:   true,
			wantDesc:      "my-session",
			wantLocked:    true,
			wantAgentKind: "claude",
			wantCCAbsent:  true,
		},
		{
			name:           "v2 schema with claude_session_* renames to agent_session_*",
			input:          `{"id":"abc","description":"d","work_dir":"/tmp/x","claude_session_id":"cc-uuid","claude_session_started":true}`,
			wantChanged:    true,
			wantDesc:       "d",
			wantLocked:     false,
			wantAgentKind:  "claude",
			wantAgentID:    "cc-uuid",
			wantAgentStart: true,
			wantConfirmed:  true,
			wantCCAbsent:   true,
		},
		{
			name:           "v3 schema gets agent_session_id_confirmed backfilled",
			input:          `{"id":"abc","description":"d","description_locked":true,"work_dir":"/tmp/x","agent_kind":"claude","agent_session_id":"uuid","agent_session_started":true}`,
			wantConfirmed:  true,
			wantChanged:    true,
			wantDesc:       "d",
			wantLocked:     true,
			wantAgentKind:  "claude",
			wantAgentID:    "uuid",
			wantAgentStart: true,
			wantCCAbsent:   true,
		},
		{
			name:           "existing description takes precedence over legacy name",
			input:          `{"id":"abc","name":"old","description":"new","work_dir":"/tmp/x"}`,
			wantChanged:    true,
			wantDesc:       "new",
			wantLocked:     false,
			wantNameAbsent: true,
			wantAgentKind:  "claude",
			wantCCAbsent:   true,
		},
		{
			name:          "non-claude agent_kind is preserved (future adapters)",
			input:         `{"id":"abc","description":"d","work_dir":"/tmp/x","agent_kind":"codex","agent_session_id":"cx-uuid"}`,
			wantChanged:   false,
			wantDesc:      "d",
			wantAgentKind: "codex",
			wantAgentID:   "cx-uuid",
			wantCCAbsent:  true,
		},
		{
			// The pre-field records are the ones the backfill is for. A record
			// this jind-ai wrote carries the key at either value, so a false
			// one must survive: flipping it back would resume an id no agent
			// has ever named.
			name:           "v4 schema with the confirmation false is untouched",
			input:          `{"id":"abc","description":"d","description_locked":true,"work_dir":"/tmp/x","agent_kind":"codex","agent_session_id":"uuid","agent_session_started":true,"agent_session_id_confirmed":false}`,
			wantChanged:    false,
			wantDesc:       "d",
			wantLocked:     true,
			wantAgentKind:  "codex",
			wantAgentID:    "uuid",
			wantAgentStart: true,
			wantConfirmed:  false,
			wantCCAbsent:   true,
		},
		{
			// Nothing was ever started, so there is no id an agent could have
			// reported and nothing to backfill.
			name:          "a record that never started an agent is untouched",
			input:         `{"id":"abc","description":"d","description_locked":true,"work_dir":"/tmp/x","agent_kind":"codex","agent_session_id":"uuid"}`,
			wantChanged:   false,
			wantDesc:      "d",
			wantLocked:    true,
			wantAgentKind: "codex",
			wantAgentID:   "uuid",
			wantConfirmed: false,
			wantCCAbsent:  true,
		},
		{
			name:    "broken JSON returns an error",
			input:   `{not json`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, changed, err := migrateSessionJSON([]byte(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tc.wantChanged)
			}

			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("output is not valid JSON: %v", err)
			}

			if desc, _ := got["description"].(string); desc != tc.wantDesc {
				t.Errorf("description = %q, want %q", desc, tc.wantDesc)
			}
			locked, _ := got["description_locked"].(bool)
			if locked != tc.wantLocked {
				t.Errorf("description_locked = %v, want %v", locked, tc.wantLocked)
			}
			if tc.wantNameAbsent {
				if _, ok := got["name"]; ok {
					t.Errorf("name field should be removed, still present: %v", got["name"])
				}
			}
			if kind, _ := got["agent_kind"].(string); kind != tc.wantAgentKind {
				t.Errorf("agent_kind = %q, want %q", kind, tc.wantAgentKind)
			}
			if id, _ := got["agent_session_id"].(string); id != tc.wantAgentID {
				t.Errorf("agent_session_id = %q, want %q", id, tc.wantAgentID)
			}
			if confirmed, _ := got["agent_session_id_confirmed"].(bool); confirmed != tc.wantConfirmed {
				t.Errorf("agent_session_id_confirmed = %v, want %v", confirmed, tc.wantConfirmed)
			}
			if started, _ := got["agent_session_started"].(bool); started != tc.wantAgentStart {
				t.Errorf("agent_session_started = %v, want %v", started, tc.wantAgentStart)
			}
			if tc.wantCCAbsent {
				if _, ok := got["claude_session_id"]; ok {
					t.Errorf("claude_session_id should be removed, still present: %v", got["claude_session_id"])
				}
				if _, ok := got["claude_session_started"]; ok {
					t.Errorf("claude_session_started should be removed, still present: %v", got["claude_session_started"])
				}
			}
		})
	}
}

func TestMigrateSessionJSON_Idempotent(t *testing.T) {
	// A v1 record: exercise both migration steps + verify second pass is a no-op.
	input := []byte(`{"id":"abc","name":"legacy","work_dir":"/tmp/x","claude_session_id":"cc-uuid","claude_session_started":true}`)

	first, changed1, err := migrateSessionJSON(input)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if !changed1 {
		t.Fatal("first migrate should report changed=true")
	}

	second, changed2, err := migrateSessionJSON(first)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if changed2 {
		t.Fatal("second migrate should report changed=false (idempotent)")
	}
	if string(second) != string(first) {
		t.Errorf("second migrate mutated output:\nfirst=%s\nsecond=%s", first, second)
	}
}
