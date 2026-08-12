package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three adapters each have their own tests for what they do with a model.
// None of them can fail when Manager stops handing one over: an adapter asked
// with Model:"" answers correctly, so a dropped field looks like a session that
// named no model.
//
// So the seam has to sit where the value is resolved. This goes from a Session
// through snapshotForSpawn into the adapter, which is the whole path Manager
// owns — dropping the field from either struct fails here.
func TestBuildAgentShellCmd_HandsTheAdapterTheSessionsModel(t *testing.T) {
	mgr, _, _ := newTestManager(t)

	var got SpawnOptions
	mgr.SetAgentResolver(&fakeAgentResolver{agents: map[string]Agent{
		"probe": &fakeAgent{spawnFn: func(opts SpawnOptions) SpawnPlan {
			got = opts
			return SpawnPlan{Command: "true"}
		}},
	}})

	dir := t.TempDir()
	sess := &Session{ID: "s1", AgentKind: "probe", WorkDir: dir, Model: "opus"}
	if _, err := mgr.buildAgentShellCmd(snapshotForSpawn(sess, dir, dir)); err != nil {
		t.Fatalf("buildAgentShellCmd: %v", err)
	}

	if got.Model != "opus" {
		t.Errorf("SpawnOptions.Model = %q, want %q — the session's model never reached the adapter", got.Model, "opus")
	}
}

// ReserveCreation is where a --model reaching the daemon stops being a request
// and becomes the record every later respawn is built from, so the value has to
// land on the Session and survive a trip through the store. Without the second
// half a daemon restart would resume the session on the agent's default model
// and say nothing about the change.
func TestReserveCreation_PersistsTheModel(t *testing.T) {
	mgr, _, _ := newTestManager(t)

	sess, info, err := mgr.ReserveCreation(CreateOptions{WorkDir: t.TempDir(), Model: "anthropic/opus"})
	if err != nil {
		t.Fatalf("ReserveCreation: %v", err)
	}
	if sess.Model != "anthropic/opus" {
		t.Errorf("Session.Model = %q, want %q", sess.Model, "anthropic/opus")
	}
	if info.Model != "anthropic/opus" {
		t.Errorf("Info.Model = %q, want %q — the create response cannot report the model", info.Model, "anthropic/opus")
	}

	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(*sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Model != "anthropic/opus" {
		t.Errorf("reloaded Model = %q, want %q — a daemon restart would lose it", loaded.Model, "anthropic/opus")
	}

	// The key on disk, not only the value: records outlive the version that
	// wrote them and migrateSessionJSON reads them as text, so a struct that
	// round-trips through itself agrees with whatever tag it happens to carry.
	raw, err := os.ReadFile(filepath.Join(dir, sess.ID+".json"))
	if err != nil {
		t.Fatalf("reading the record back: %v", err)
	}
	if !strings.Contains(string(raw), `"model": "anthropic/opus"`) {
		t.Errorf("the record does not spell the key `model`:\n%s", raw)
	}
}
