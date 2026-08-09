package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MikcleGrok/uni-chat-sdk/pkg/protocol"
)

func TestConfigRoundTripUsesGenericType(t *testing.T) {
	type config struct {
		Name string `json:"name"`
	}
	dir := t.TempDir()
	if err := SaveConfig(dir, config{Name: "engine"}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig[config](dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "engine" {
		t.Fatalf("config = %+v", got)
	}
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
	if err := SaveConfig(dir, config{Name: "updated"}); err != nil {
		t.Fatal(err)
	}
	updated, err := LoadConfig[config](dir)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "updated" {
		t.Fatalf("updated config = %+v", updated)
	}
}

func TestConfigStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := State{Cursors: map[string]string{"c1": "p9"}, Watch: []string{"unisender/dev"}, LastCheck: "2026-07-17T10:00:00Z"}
	if err := SaveState(dir, st); err != nil {
		t.Fatal(err)
	}
	gotSt, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if gotSt.Cursors["c1"] != "p9" || len(gotSt.Watch) != 1 || gotSt.LastCheck == "" {
		t.Fatalf("state = %+v", gotSt)
	}
}

func TestLoadStateMissingIsEmpty(t *testing.T) {
	st, err := LoadState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if st.Cursors == nil {
		t.Fatal("Cursors must be a non-nil map even when state.json is absent")
	}
	if st.Pending == nil {
		t.Fatal("Pending must be a non-nil map even when state.json is absent")
	}
	if st.Activity == nil {
		t.Fatal("Activity must be a non-nil map even when state.json is absent")
	}
}

func TestRecordActivityIsMonotonic(t *testing.T) {
	s := State{}
	if !RecordActivity(&s, "c1", "2026-07-31T10:00:00+02:00") {
		t.Fatal("first activity was not recorded")
	}
	if RecordActivity(&s, "c1", "2026-07-31T07:59:59Z") {
		t.Fatal("older activity moved the index backwards")
	}
	if s.Activity["c1"] != "2026-07-31T08:00:00Z" {
		t.Fatalf("activity = %q, want normalized first timestamp", s.Activity["c1"])
	}
	if !RecordActivity(&s, "c1", "2026-07-31T08:00:01Z") || s.Activity["c1"] != "2026-07-31T08:00:01Z" {
		t.Fatalf("newer activity was not recorded: %+v", s.Activity)
	}
}

func TestApplyPendingLifecycle(t *testing.T) {
	s := State{}
	first := protocol.CheckItem{ChannelID: "c1", ChannelRef: "team/dev", PostID: "p1", CreatedAt: "2026-07-31T09:00:00Z", Addressed: true}
	ApplyPending(&s, first)
	if s.Pending["c1"].PostID != "p1" {
		t.Fatalf("after first addressed = %+v, want p1", s.Pending)
	}
	ApplyPending(&s, protocol.CheckItem{ChannelID: "c1", PostID: "noise"})
	if s.Pending["c1"].PostID != "p1" {
		t.Fatalf("unrelated message changed pending = %+v", s.Pending)
	}
	ApplyPending(&s, protocol.CheckItem{ChannelID: "c1", ChannelRef: "team/dev", PostID: "p2", CreatedAt: "2026-07-31T09:30:00Z", Addressed: true})
	if s.Pending["c1"].PostID != "p2" {
		t.Fatalf("second addressed = %+v, want latest p2", s.Pending)
	}
	ApplyPending(&s, protocol.CheckItem{ChannelID: "c1", PostID: "p3", CreatedAt: "2026-07-31T10:00:00Z", Own: true})
	if _, ok := s.Pending["c1"]; ok {
		t.Fatalf("own reply did not clear pending = %+v", s.Pending)
	}
	ApplyPending(&s, protocol.CheckItem{ChannelID: "c1", ChannelRef: "team/dev", PostID: "p4", CreatedAt: "2026-07-31T11:00:00Z", Addressed: true})
	if s.Pending["c1"].PostID != "p4" {
		t.Fatalf("address after reply = %+v, want p4", s.Pending)
	}
}

func TestMergePendingDoesNotReopenAfterConcurrentResolution(t *testing.T) {
	snapshot := State{
		Pending:         map[string]protocol.CheckItem{"c1": {ChannelID: "c1", ChannelRef: "team/dev", PostID: "old", CreatedAt: "2026-07-31T09:00:00Z"}},
		PendingRevision: map[string]uint64{"c1": 1},
		Revision:        1,
	}
	current := State{
		Pending:         map[string]protocol.CheckItem{"c1": snapshot.Pending["c1"]},
		PendingRevision: map[string]uint64{"c1": 1},
		Revision:        1,
	}
	ApplyPending(&current, protocol.CheckItem{ChannelID: "c1", PostID: "reply", Own: true, CreatedAt: "2026-07-31T10:00:00Z"})
	MergePending(&current, []protocol.CheckItem{{ChannelID: "c1", ChannelRef: "team/dev", PostID: "old", Addressed: true, CreatedAt: "2026-07-31T09:00:00Z"}})
	if _, ok := current.Pending["c1"]; ok {
		t.Fatalf("stale check reopened pending = %+v", current.Pending)
	}
}

func TestMergePendingHonorsResolutionTimestampAfterSnapshot(t *testing.T) {
	s := State{
		PendingRevision:   map[string]uint64{"c1": 2},
		PendingResolvedAt: map[string]string{"c1": "2026-07-31T10:00:00Z"},
		Revision:          2,
	}
	MergePending(&s, []protocol.CheckItem{{ChannelID: "c1", ChannelRef: "team/dev", PostID: "old", Addressed: true, CreatedAt: "2026-07-31T09:00:00Z"}})
	if _, ok := s.Pending["c1"]; ok {
		t.Fatalf("stale response reopened resolved pending = %+v", s.Pending)
	}
}

func TestMergePendingOrdersFractionalAndOffsetTimestamps(t *testing.T) {
	s := State{PendingResolvedAt: map[string]string{"c1": "2026-07-31T10:00:00+02:00"}}
	accepted := MergePending(&s, []protocol.CheckItem{
		{ChannelID: "c1", ChannelRef: "team/dev", PostID: "old", Addressed: true, CreatedAt: "2026-07-31T08:00:00Z"},
		{ChannelID: "c1", ChannelRef: "team/dev", PostID: "new", Addressed: true, CreatedAt: "2026-07-31T08:00:00.000000001Z"},
	})
	if len(accepted) != 1 || accepted[0].PostID != "new" || s.Pending["c1"].PostID != "new" {
		t.Fatalf("accepted = %+v, pending = %+v, want only post-resolution new", accepted, s.Pending)
	}
}

func TestApplyPendingUsesPostIDOnlyForEqualInstants(t *testing.T) {
	s := State{}
	ApplyPending(&s, protocol.CheckItem{ChannelID: "c1", ChannelRef: "team/dev", PostID: "p2", Addressed: true, CreatedAt: "2026-07-31T10:00:00+02:00"})
	if ApplyPending(&s, protocol.CheckItem{ChannelID: "c1", ChannelRef: "team/dev", PostID: "p1", Addressed: true, CreatedAt: "2026-07-31T08:00:00.000Z"}) {
		t.Fatal("an older/equal post must not replace the pending item")
	}
	if !ApplyPending(&s, protocol.CheckItem{ChannelID: "c1", ChannelRef: "team/dev", PostID: "p3", Addressed: true, CreatedAt: "2026-07-31T08:00:00.000Z"}) {
		t.Fatal("a higher post ID at the same instant must replace the pending item")
	}
}

func TestRecordCursorRejectsConflictingOpaqueSuccessors(t *testing.T) {
	s := State{Cursors: map[string]string{"c1": "base"}}
	if !RecordCursor(&s, "c1", "base", "branch-a") {
		t.Fatal("first opaque successor was not recorded")
	}
	if RecordCursor(&s, "c1", "base", "branch-b") {
		t.Fatal("conflicting opaque successor must remain unordered")
	}
	if AdvanceCursor(&s, "c1", "branch-b") {
		t.Fatal("unknown opaque successor must not advance the cursor")
	}
	if !AdvanceCursor(&s, "c1", "branch-a") || s.Cursors["c1"] != "branch-a" {
		t.Fatalf("proven successor was not accepted: %+v", s.Cursors)
	}
}

func TestAdvanceCursorRequiresProvenEmptyBaseSuccessor(t *testing.T) {
	s := State{Cursors: map[string]string{"c1": ""}}
	if !RecordCursor(&s, "c1", "", "p1") {
		t.Fatal("baseline successor was not recorded")
	}
	if RecordCursor(&s, "c1", "", "p2") {
		t.Fatal("concurrent conflicting baseline successor must remain unproven")
	}
	if AdvanceCursor(&s, "c1", "p2") {
		t.Fatal("ACK for rejected baseline branch must not advance the cursor")
	}
	if s.Cursors["c1"] != "" {
		t.Fatalf("rejected baseline branch changed cursor to %q", s.Cursors["c1"])
	}
	if !AdvanceCursor(&s, "c1", "p1") || s.Cursors["c1"] != "p1" {
		t.Fatalf("proven baseline successor was not accepted: %+v", s.Cursors)
	}
}

func TestLoadStateDropsUnanchorablePending(t *testing.T) {
	dir := t.TempDir()
	if err := SaveState(dir, State{Pending: map[string]protocol.CheckItem{
		"empty-post": {ChannelID: "c1", ChannelRef: "team/dev"},
		"empty-ref":  {ChannelID: "c2", PostID: "p2"},
		"valid":      {ChannelID: "c3", ChannelRef: "team/dev", PostID: "p3"},
	}}); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Pending) != 1 || st.Pending["valid"].PostID != "p3" {
		t.Fatalf("pending = %+v, want only the anchored item", st.Pending)
	}
}

func TestLockSerializesStateWriters(t *testing.T) {
	dir := t.TempDir()
	release, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	acquired := make(chan struct{})
	go func() {
		close(started)
		releaseOther, lockErr := Lock(dir)
		if lockErr == nil {
			close(acquired)
			releaseOther()
		}
	}()
	<-started
	select {
	case <-acquired:
		t.Fatal("second state writer acquired the lock before the first released it")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second state writer did not acquire the lock after release")
	}
}
