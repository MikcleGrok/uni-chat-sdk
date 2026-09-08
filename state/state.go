// Package state owns per-engine on-disk config and cursor state and the pure
// cursor-diff logic. Engines read and write their own state directories.
package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/MikcleGrok/uni-chat-sdk/pkg/protocol"
)

// State is ~/.uni-chat/state.json — cursors, the watch list, activity index,
// pending projection, and optimistic metadata for concurrent checks.
type State struct {
	Cursors           map[string]string             `json:"cursors"`
	CursorVersions    map[string]map[string]uint64  `json:"cursor_versions,omitempty"`
	CursorSuccessors  map[string]map[string]string  `json:"cursor_successors,omitempty"`
	Activity          map[string]string             `json:"activity,omitempty"`
	Watch             []string                      `json:"watch"`
	Pending           map[string]protocol.CheckItem `json:"pending,omitempty"`
	Revision          uint64                        `json:"revision,omitempty"`
	PendingRevision   map[string]uint64             `json:"pending_revision,omitempty"`
	PendingResolvedAt map[string]string             `json:"pending_resolved_at,omitempty"`
	LastCheck         string                        `json:"last_check,omitempty"` // RFC3339
}

func LoadConfig[T any](dir string) (T, error) {
	var c T
	b, err := os.ReadFile(filepath.Join(dir, "config.json")) // #nosec G304 -- the config path is derived from the private application directory.
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

func SaveConfig[T any](dir string, c T) error {
	return writeJSON(filepath.Join(dir, "config.json"), c)
}

// LoadState returns a zero State (with a non-nil Cursors map) when the file is
// absent, so callers can index Cursors without a nil check.
func LoadState(dir string) (State, error) {
	s := State{Cursors: map[string]string{}, CursorVersions: map[string]map[string]uint64{}, CursorSuccessors: map[string]map[string]string{}, Activity: map[string]string{}, Pending: map[string]protocol.CheckItem{}, PendingRevision: map[string]uint64{}, PendingResolvedAt: map[string]string{}}
	b, err := os.ReadFile(filepath.Join(dir, "state.json")) // #nosec G304 -- the state path is derived from the private application directory.
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if s.Cursors == nil {
		s.Cursors = map[string]string{}
	}
	if s.CursorVersions == nil {
		s.CursorVersions = map[string]map[string]uint64{}
	}
	if s.CursorSuccessors == nil {
		s.CursorSuccessors = map[string]map[string]string{}
	}
	if s.Activity == nil {
		s.Activity = map[string]string{}
	}
	if s.Pending == nil {
		s.Pending = map[string]protocol.CheckItem{}
	}
	if s.PendingRevision == nil {
		s.PendingRevision = map[string]uint64{}
	}
	if s.PendingResolvedAt == nil {
		s.PendingResolvedAt = map[string]string{}
	}
	for _, revision := range s.PendingRevision {
		if revision > s.Revision {
			s.Revision = revision
		}
	}
	for channelID, item := range s.Pending {
		if !validPending(item) || strings.TrimSpace(channelID) == "" {
			delete(s.Pending, channelID)
		}
	}
	return s, nil
}

func SaveState(dir string, s State) error {
	return writeJSON(filepath.Join(dir, "state.json"), s)
}

// ApplyPending updates the one-item-per-channel projection. Own posts always
// close the projection, while addressed incoming posts replace it with the
// latest unanswered request. Other posts leave it unchanged.
func ApplyPending(s *State, item protocol.CheckItem) bool {
	if s.Pending == nil {
		s.Pending = map[string]protocol.CheckItem{}
	}
	if s.PendingRevision == nil {
		s.PendingRevision = map[string]uint64{}
	}
	if s.PendingResolvedAt == nil {
		s.PendingResolvedAt = map[string]string{}
	}
	if item.ChannelID == "" {
		return false
	}
	if item.Own {
		if current, ok := s.Pending[item.ChannelID]; ok && compareItems(item, current) <= 0 {
			return false
		}
		if resolvedAt := s.PendingResolvedAt[item.ChannelID]; resolvedAt != "" && compareItemToResolution(item, resolvedAt, "") <= 0 {
			return false
		}
		delete(s.Pending, item.ChannelID)
		resolvedAt := item.CreatedAt
		if resolvedAt == "" {
			resolvedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		s.PendingResolvedAt[item.ChannelID] = resolvedAt
		touchPending(s, item.ChannelID)
		return true
	}
	if item.Addressed && validPending(item) {
		if resolvedAt := s.PendingResolvedAt[item.ChannelID]; resolvedAt != "" && compareItemToResolution(item, resolvedAt, "") <= 0 {
			return false
		}
		if current, ok := s.Pending[item.ChannelID]; ok && compareItems(item, current) <= 0 {
			return false
		}
		s.Pending[item.ChannelID] = item
		delete(s.PendingResolvedAt, item.ChannelID)
		touchPending(s, item.ChannelID)
		return true
	}
	return false
}

// MergePending applies all check events against the state loaded under the
// writer lock. A concurrent revision is not a reason to discard the whole
// batch: addressed events are filtered against the current pending/resolution
// frontier, while unrelated watch events remain new notification events.
func MergePending(current *State, items []protocol.CheckItem) []protocol.CheckItem {
	byChannel := map[string][]protocol.CheckItem{}
	order := make([]string, 0, len(items))
	for _, item := range items {
		if _, seen := byChannel[item.ChannelID]; !seen {
			order = append(order, item.ChannelID)
		}
		byChannel[item.ChannelID] = append(byChannel[item.ChannelID], item)
	}
	accepted := make([]protocol.CheckItem, 0, len(items))
	for _, channelID := range order {
		batch := byChannel[channelID]
		sort.SliceStable(batch, func(i, j int) bool { return compareItems(batch[i], batch[j]) < 0 })
		for _, item := range batch {
			if item.Own || item.Addressed {
				if ApplyPending(current, item) {
					accepted = append(accepted, item)
				}
				continue
			}
			accepted = append(accepted, item)
		}
	}
	return accepted
}

func compareItems(left, right protocol.CheckItem) int {
	if cmp, leftValid, rightValid := compareTimestamps(left.CreatedAt, right.CreatedAt); leftValid || rightValid {
		if leftValid != rightValid {
			if leftValid {
				return 1
			}
			return -1
		}
		if cmp != 0 {
			return cmp
		}
	}
	return strings.Compare(left.PostID, right.PostID)
}

func compareItemToResolution(item protocol.CheckItem, resolvedAt, resolvedID string) int {
	cmp, itemValid, resolutionValid := compareTimestamps(item.CreatedAt, resolvedAt)
	if !itemValid || !resolutionValid {
		if resolvedID != "" && item.PostID != "" {
			return strings.Compare(item.PostID, resolvedID)
		}
		return 0
	}
	if cmp != 0 {
		return cmp
	}
	if resolvedID == "" || item.PostID == "" {
		return 0
	}
	return strings.Compare(item.PostID, resolvedID)
}

func compareTimestamps(left, right string) (int, bool, bool) {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, left)
	rightTime, rightErr := time.Parse(time.RFC3339Nano, right)
	leftValid := leftErr == nil
	rightValid := rightErr == nil
	if !leftValid || !rightValid {
		return 0, leftValid, rightValid
	}
	return leftTime.Compare(rightTime), true, true
}

// RecordCursor records only a cursor successor observed from the exact active
// cursor used by a check. Mattermost post IDs are opaque, so two conflicting
// successors from one base are deliberately left unorderable.
func RecordCursor(s *State, channelID, base, candidate string) bool {
	if channelID == "" || candidate == "" || base == candidate {
		return false
	}
	if s.CursorVersions == nil {
		s.CursorVersions = map[string]map[string]uint64{}
	}
	if s.CursorSuccessors == nil {
		s.CursorSuccessors = map[string]map[string]string{}
	}
	versions := s.CursorVersions[channelID]
	if versions == nil {
		versions = map[string]uint64{}
		s.CursorVersions[channelID] = versions
	}
	successors := s.CursorSuccessors[channelID]
	if successors == nil {
		successors = map[string]string{}
		s.CursorSuccessors[channelID] = successors
	}
	if successor, exists := successors[base]; exists {
		return successor == candidate
	}
	if _, known := versions[candidate]; known {
		return false
	}
	baseVersion := versions[base]
	if base != "" {
		if _, known := versions[base]; !known {
			versions[base] = 0
		}
	}
	versions[candidate] = baseVersion + 1
	successors[base] = candidate
	return true
}

// AdvanceCursor accepts a Mattermost cursor only when its order was proven by
// RecordCursor. Unknown opaque values are ignored rather than compared by text.
func AdvanceCursor(s *State, channelID, candidate string) bool {
	if channelID == "" || candidate == "" {
		return false
	}
	current := s.Cursors[channelID]
	if current == candidate {
		return false
	}
	successors := s.CursorSuccessors[channelID]
	if successor, proven := successors[current]; !proven || successor != candidate {
		return false
	}
	if s.Cursors == nil {
		s.Cursors = map[string]string{}
	}
	s.Cursors[channelID] = candidate
	return true
}

// RecordActivity stores the newest valid activity timestamp for a channel. The
// activity index is independent from cursors: it is updated when posts are
// observed, while cursors still advance only after ack.
func RecordActivity(s *State, channelID, candidate string) bool {
	if strings.TrimSpace(channelID) == "" || strings.TrimSpace(candidate) == "" {
		return false
	}
	candidateTime, err := time.Parse(time.RFC3339Nano, candidate)
	if err != nil {
		return false
	}
	if s.Activity == nil {
		s.Activity = map[string]string{}
	}
	current := s.Activity[channelID]
	if current != "" {
		currentTime, err := time.Parse(time.RFC3339Nano, current)
		if err == nil && !candidateTime.After(currentTime) {
			return false
		}
	}
	s.Activity[channelID] = candidateTime.UTC().Format(time.RFC3339Nano)
	return true
}

func touchPending(s *State, channelID string) {
	s.Revision++
	s.PendingRevision[channelID] = s.Revision
}

func validPending(item protocol.CheckItem) bool {
	return strings.TrimSpace(item.ChannelID) != "" && strings.TrimSpace(item.ChannelRef) != "" && strings.TrimSpace(item.PostID) != ""
}

// defaultLockTimeout bounds Lock's wait for the cross-process advisory lock.
// It is chosen to be comfortably larger than this lock's normal hold time (a
// one-shot engine request acquires it, does its work, and releases) and
// comfortably smaller than the protocol layer's own connection timeouts
// (pkg/protocol.serveConnectionTimeout = 120s): a caller still waiting past
// that point has already lost its own client anyway, so failing this lock
// first — with a diagnosis naming the lock file and the elapsed deadline —
// is more actionable than an indefinite hang that eventually surfaces as an
// unrelated timeout further up the stack.
const defaultLockTimeout = 30 * time.Second

// lockPollInterval is the pause between LOCK_EX|LOCK_NB retries. Short
// enough that a lock released just after a failed attempt is picked up
// quickly; long enough not to busy-spin.
const lockPollInterval = 10 * time.Millisecond

// Lock takes the cross-process lock shared by the one-shot engine requests,
// bounded by defaultLockTimeout. The returned function releases both the
// advisory lock and its file handle. Equivalent to
// LockContext with a context.WithTimeout(context.Background(), defaultLockTimeout).
func Lock(dir string) (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultLockTimeout)
	defer cancel()
	return LockContext(ctx, dir)
}

// LockContext is Lock's context-aware counterpart. Unlike the previous
// unconditional syscall.Flock(..., LOCK_EX) — a blocking call with no
// deadline and no way to observe cancellation, so a wedged or crashed
// holder's lock file wedged every future caller forever too — this polls
// with LOCK_EX|LOCK_NB and returns as soon as the lock is acquired, ctx is
// done, or (for a caller-supplied ctx with no deadline, e.g.
// context.Background()) never, matching the honest meaning of that ctx. The
// returned function releases both the advisory lock and its file handle.
func LockContext(ctx context.Context, dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil { // an existing dir's mode is not narrowed by MkdirAll alone.
		return nil, err
	}
	lockPath := filepath.Join(dir, "state.lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- the lock path is derived from the private application directory.
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil { // an existing file's mode is not narrowed by the O_CREATE open mode alone.
		_ = file.Close()
		return nil, err
	}
	for {
		flockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if flockErr == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(flockErr, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, flockErr
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			if deadline, ok := ctx.Deadline(); ok {
				return nil, fmt.Errorf("state: lock %s not acquired before deadline %s (%w)", lockPath, deadline.Format(time.RFC3339), ctx.Err())
			}
			return nil, fmt.Errorf("state: lock %s not acquired: %w", lockPath, ctx.Err())
		case <-time.After(lockPollInterval):
		}
	}
}

// writeJSON writes v atomically (temp file + rename) with 0600 perms.
// writeJSON writes v atomically (temp file + rename) with 0600 perms on the
// final file. It defends against the same threat docs/security.md names —
// "права 0700/0600 по ошибке оказались шире (баг, неверный umask,
// восстановление из бэкапа)" — for every path it touches:
//   - the target directory's mode is explicitly narrowed even if it already
//     existed (os.MkdirAll alone does not change an existing directory's
//     mode);
//   - the temp file is created via os.CreateTemp (a fresh, non-predictable
//     name in the same directory, retried internally on any collision)
//     rather than a fixed path+".tmp": a predictable path can already exist
//     with wider permissions from exactly that threat model, and
//     os.WriteFile's mode argument is silently ignored by O_TRUNC against
//     such a pre-existing file, so the wide mode would have survived
//     straight through the rename. A fresh file cannot inherit anyone
//     else's stale permissions, and two concurrent writers can no longer
//     collide on the same fixed tmp path either.
func writeJSON(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil { // narrow an already-existing dir's mode too.
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil { // explicit, not relied upon as merely CreateTemp's current default.
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	removeTmp = false
	return nil
}
