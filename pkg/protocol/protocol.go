// Package protocol is the wire contract between uni-chat (client) and uni-chatd
// (daemon): JSON request/response over a 0600 Unix socket, one request per
// connection. It depends on the standard library only, so both binaries agree
// on the payloads without dragging in the Mattermost types.
package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	dirEnv  = "UNI_CHAT_DIR"
	sockEnv = "UNI_CHAT_SOCK"
)

// Dir is ~/.uni-chat (config.json, state.json, the socket), overridable via
// UNI_CHAT_DIR for tests.
func Dir() string {
	if v := strings.TrimSpace(os.Getenv(dirEnv)); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".uni-chat")
}

// SocketPath is Dir()/uni-chatd.sock, overridable via UNI_CHAT_SOCK.
func SocketPath() string {
	if v := strings.TrimSpace(os.Getenv(sockEnv)); v != "" {
		return v
	}
	return filepath.Join(Dir(), "uni-chatd.sock")
}

// Request is one client command. Args is the command-specific payload.
type Request struct {
	Cmd  string          `json:"cmd"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Response is the daemon's single reply. OK=false carries a human error.
type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// CheckItem is one message the client renders: a new post to notify about
// (check), a post from a loaded scrollback page (history), or the echo of a
// post we just sent (send).
type CheckItem struct {
	ChannelID  string `json:"channel_id"`
	ChannelRef string `json:"channel_ref"` // "<team>/<channel>" or the adapter's DM routing ref
	Sender     string `json:"sender"`      // username of the poster
	Message    string `json:"message"`
	PostID     string `json:"post_id"`
	Kind       string `json:"kind"`                 // "mention" | "dm" | "watch" | "activity"
	Own        bool   `json:"own,omitempty"`        // true when the current user authored the post
	Addressed  bool   `json:"addressed,omitempty"`  // true when the post addresses the current user
	CreatedAt  string `json:"created_at,omitempty"` // RFC3339, UTC
	// ThreadRootID is the id of the post this one replies to, empty when the
	// post belongs to no thread. It is a post id, not a channel id: the daemon
	// prefixes ChannelID/ChannelRef with the engine name and never touches post
	// ids, so this value stays directly comparable with PostID of the other
	// items in the same channel — which is what the client's thread grouping
	// relies on. An engine whose platform has no threads leaves it empty.
	ThreadRootID string   `json:"thread_root_id,omitempty"`
	DeepLink     string   `json:"deep_link"`
	WebLink      string   `json:"web_link"`
	Reactions    []string `json:"reactions,omitempty"`     // deduped emoji names, e.g. ["white_check_mark"]
	OwnReactions []string `json:"own_reactions,omitempty"` // emoji names put by the current user
}

// CheckData is the "check" reply: new items plus the per-channel cursors the
// client must ack (channel id -> latest seen post id) once it has notified.
type CheckData struct {
	Items   []CheckItem       `json:"items"`
	Cursors map[string]string `json:"cursors"`
	Partial bool              `json:"partial,omitempty"`
	Errors  []SyncError       `json:"errors,omitempty"`
}

// CheckArgs is the optional payload of the "check" verb. IncludeOwn asks the
// engine to keep the current user's own posts in the reply instead of dropping
// them; the zero value is the historical behavior, which is what the scheduled
// uni-chat notifier keeps sending.
type CheckArgs struct {
	IncludeOwn bool `json:"include_own,omitempty"`
	Refresh    bool `json:"refresh,omitempty"`
}

type CheckCursorsArgs struct {
	Cursors    map[string]string `json:"cursors"`
	IncludeOwn bool              `json:"include_own,omitempty"`
}

// SyncError identifies an engine- or channel-scoped read failure. Empty
// ChannelID/ChannelRef means the error prevented the engine from listing its
// channels or authenticating.
type SyncError struct {
	Engine     string `json:"engine"`
	ChannelID  string `json:"channel_id,omitempty"`
	ChannelRef string `json:"channel_ref,omitempty"`
	Error      string `json:"error"`
}

// SyncData is the complete-read result. Partial is true for any scoped error
// or truncation; Truncated specifically means a pagination safety limit was
// reached before Mattermost reported the end of history.
type SyncData struct {
	Items     []CheckItem `json:"items"`
	Partial   bool        `json:"partial"`
	Truncated bool        `json:"truncated"`
	Errors    []SyncError `json:"errors,omitempty"`
}

// AckArgs commits the cursors returned by a prior "check".
type AckArgs struct {
	Cursors map[string]string `json:"cursors"`
}

// PostArgs / PostData back the "post" command.
type PostArgs struct {
	Channel string `json:"channel"`
	Text    string `json:"text"`
	// RootPostID makes this a reply inside an existing thread instead of a new
	// top-level message. Empty is the ordinary channel post, which is what the
	// CLI's "post" verb sends when --thread is not given.
	RootPostID string `json:"root_post_id,omitempty"`
}
type PostData struct {
	Permalink string `json:"permalink"`
	ChannelID string `json:"channel_id,omitempty"`
	PostID    string `json:"post_id,omitempty"`
}

// WatchArgs / WatchListData back the "watch_*" commands.
type WatchArgs struct {
	Channel string `json:"channel"`
}
type WatchListData struct {
	Channels []string `json:"channels"`
}

// ChannelsArgs controls the channel list returned by an engine. Since is a
// common cutoff supplied by the client; Mode is the one global channel mode
// the router decides before building any per-engine request, and every
// enabled engine receives the identical value. Empty values preserve the
// original channels RPC, whose behavior is watch.
type ChannelsArgs struct {
	Since string `json:"since,omitempty"` // RFC3339 cutoff for recent mode
	Mode  string `json:"mode,omitempty"`  // "watch" | "recent"
}

// ChannelInfo is one browsable channel: the engine it belongs to, its opaque
// platform id, the routing ref, an optional human-readable display name, and
// why it is on the list. Kind reuses
// CheckItem.Kind's vocabulary but means a property of the channel here, not a
// reason to notify: "dm" is a direct or group conversation, "watch" a channel
// on the watch list, "mention" an ordinary channel that reached the list
// because we were mentioned in it, and "activity" one included by recent
// activity without a pending mention.
type ChannelInfo struct {
	Engine       string     `json:"engine"`
	ChannelID    string     `json:"channel_id"`
	ChannelRef   string     `json:"channel_ref"`
	DisplayName  string     `json:"display_name,omitempty"`
	Kind         string     `json:"kind"` // "mention" | "dm" | "watch" | "activity"
	Always       bool       `json:"always,omitempty"`
	LastActivity string     `json:"last_activity,omitempty"` // RFC3339, UTC
	Pending      *CheckItem `json:"pending,omitempty"`
}

// ChannelsData is the "channels" reply: every channel this client may browse.
// Mode is the single channel mode the whole registry is set to — it is not per
// engine and cannot be: the router computes it once per request. Engines never
// set it; the router fills it in on the merged reply.
type ChannelsData struct {
	Channels []ChannelInfo `json:"channels"`
	Mode     string        `json:"mode,omitempty"`
	Partial  bool          `json:"partial,omitempty"`
	Engines  []string      `json:"engines,omitempty"`
}

// HistoryArgs asks for one page of scrollback. Before is a post id: an empty
// Before means "the most recent page". Limit is the page size (0 => the
// adapter's default).
//
// Exactly one of ChannelID and Channel is set. ChannelID is an opaque
// platform id — what the TUI already holds from a prior "channels"/"check"/
// "history" round trip. Channel is a human-readable "<team>/<channel>" ref
// (or the engine's single-team shorthand), resolved server-side the same way
// PostArgs.Channel already is — the CLI's own entry point into history, which
// (unlike the TUI) never already holds an id.
type HistoryArgs struct {
	ChannelID   string    `json:"channel_id,omitempty"`
	Channel     string    `json:"channel,omitempty"`
	Before      string    `json:"before,omitempty"`
	Limit       int       `json:"limit,omitempty"`
	SinceTime   time.Time `json:"since_time,omitempty"` // inclusive lower bound
	UntilTime   time.Time `json:"until_time,omitempty"` // exclusive upper bound
	MaxPages    int       `json:"max_pages,omitempty"`  // zero is unlimited when present
	MaxPagesSet bool      `json:"-"`
}

func (a HistoryArgs) MarshalJSON() ([]byte, error) {
	type wire struct {
		ChannelID string     `json:"channel_id,omitempty"`
		Channel   string     `json:"channel,omitempty"`
		Before    string     `json:"before,omitempty"`
		Limit     int        `json:"limit,omitempty"`
		SinceTime *time.Time `json:"since_time,omitempty"`
		UntilTime *time.Time `json:"until_time,omitempty"`
		MaxPages  *int       `json:"max_pages,omitempty"`
	}
	w := wire{ChannelID: a.ChannelID, Channel: a.Channel, Before: a.Before, Limit: a.Limit}
	if !a.SinceTime.IsZero() {
		w.SinceTime = &a.SinceTime
	}
	if !a.UntilTime.IsZero() {
		w.UntilTime = &a.UntilTime
	}
	if a.MaxPagesSet || a.MaxPages != 0 {
		w.MaxPages = &a.MaxPages
	}
	return json.Marshal(w)
}

func (a *HistoryArgs) UnmarshalJSON(data []byte) error {
	type wire struct {
		ChannelID string    `json:"channel_id"`
		Channel   string    `json:"channel"`
		Before    string    `json:"before"`
		Limit     int       `json:"limit"`
		SinceTime time.Time `json:"since_time"`
		UntilTime time.Time `json:"until_time"`
		MaxPages  *int      `json:"max_pages"`
	}
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	a.ChannelID, a.Channel, a.Before, a.Limit = w.ChannelID, w.Channel, w.Before, w.Limit
	a.SinceTime, a.UntilTime = w.SinceTime, w.UntilTime
	a.MaxPagesSet, a.MaxPages = w.MaxPages != nil, 0
	if w.MaxPages != nil {
		a.MaxPages = *w.MaxPages
	}
	return nil
}

// HistoryData is oldest-first. BeforeCursor is the server-owned cursor for the
// next older page; an empty cursor means there is no older page. HasMore stays
// available for range retrieval and legacy-compatible callers.
type HistoryData struct {
	Items        []CheckItem `json:"items"`
	HasMore      bool        `json:"has_more"`
	BeforeCursor string      `json:"before_cursor,omitempty"`
}

// SearchArgs asks for posts matching Query in an optional half-open time range.
type SearchArgs struct {
	Query     string    `json:"query"`
	Channel   string    `json:"channel,omitempty"`
	SinceTime time.Time `json:"since_time,omitempty"`
	UntilTime time.Time `json:"until_time,omitempty"`
	Author    string    `json:"author,omitempty"`
}

func (a SearchArgs) MarshalJSON() ([]byte, error) {
	type wire struct {
		Query     string     `json:"query"`
		Channel   string     `json:"channel,omitempty"`
		SinceTime *time.Time `json:"since_time,omitempty"`
		UntilTime *time.Time `json:"until_time,omitempty"`
		Author    string     `json:"author,omitempty"`
	}
	w := wire{Query: a.Query, Channel: a.Channel, Author: a.Author}
	if !a.SinceTime.IsZero() {
		w.SinceTime = &a.SinceTime
	}
	if !a.UntilTime.IsZero() {
		w.UntilTime = &a.UntilTime
	}
	return json.Marshal(w)
}

func (a *SearchArgs) UnmarshalJSON(data []byte) error {
	type wire struct {
		Query     string    `json:"query"`
		Channel   string    `json:"channel"`
		SinceTime time.Time `json:"since_time"`
		UntilTime time.Time `json:"until_time"`
		Author    string    `json:"author"`
	}
	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	a.Query, a.Channel, a.SinceTime, a.UntilTime, a.Author = w.Query, w.Channel, w.SinceTime, w.UntilTime, w.Author
	return nil
}

type SearchItem struct {
	PostID       string    `json:"post_id"`
	ChannelID    string    `json:"channel_id"`
	ChannelRef   string    `json:"channel_ref,omitempty"`
	Sender       string    `json:"sender"`
	SenderUserID string    `json:"sender_user_id,omitempty"`
	Message      string    `json:"message"`
	CreatedAt    time.Time `json:"created_at"`
	ThreadRootID string    `json:"thread_root_id,omitempty"`
	Link         string    `json:"link,omitempty"`
	Reactions    []string  `json:"reactions,omitempty"`
}

type SearchError struct {
	Engine  string `json:"engine"`
	TeamID  string `json:"team_id,omitempty"`
	TeamRef string `json:"team_ref,omitempty"`
	Error   string `json:"error"`
}

type SearchData struct {
	Items   []SearchItem  `json:"items"`
	Partial bool          `json:"partial"`
	HasMore bool          `json:"has_more"`
	Errors  []SearchError `json:"errors,omitempty"`
}

// SendArgs posts Text to a channel addressed by id. It does not replace
// "post": post takes a human-readable <engine>/<team>/<channel> ref and stays
// the CLI's verb, send takes the id the TUI already holds from "channels".
type SendArgs struct {
	ChannelID string `json:"channel_id"`
	Text      string `json:"text"`
	// RootPostID makes this a reply inside an existing thread instead of a new
	// top-level message. Empty is the ordinary channel reply. The CLI's "post"
	// verb carries the same field (PostArgs.RootPostID) and sends it non-empty
	// when --thread is given. Like every post id it crosses the daemon
	// unprefixed.
	RootPostID string `json:"root_post_id,omitempty"`
}

// SendData echoes the posted message back, so the client can render it without
// waiting for the next poll.
type SendData struct {
	Item CheckItem `json:"item"`
}

// ReactArgs adds Emoji to the post addressed by PostID in ChannelID. ChannelID
// is also the routing context, so callers must provide the engine-prefixed id
// when speaking to the daemon.
type ReactArgs struct {
	ChannelID string `json:"channel_id"`
	PostID    string `json:"post_id"`
	Emoji     string `json:"emoji"`
}

// ReactData is the platform-neutral result of adding a reaction.
type ReactData struct {
	UserID string `json:"user_id"`
	PostID string `json:"post_id"`
	Emoji  string `json:"emoji"`
}

// EditArgs replaces the text of one post in place. PostID alone does not say
// which engine owns it, so the caller supplies either an engine-prefixed
// ChannelID or the bare Engine name.
type EditArgs struct {
	ChannelID string `json:"channel_id,omitempty"`
	Engine    string `json:"engine,omitempty"`
	PostID    string `json:"post_id"`
	Text      string `json:"text"`
}

// EditData is the platform-neutral result of editing a post.
type EditData struct {
	Permalink string `json:"permalink"`
	PostID    string `json:"post_id"`
}

// DeleteArgs removes one post for the legacy TUI path. PostID alone does not
// say which engine owns it, so the caller supplies either an engine-prefixed
// ChannelID or the bare Engine name.
// The router consumes Engine and never relays it: an adapter is never told its
// own registered name (see router.channels).
type DeleteArgs struct {
	ChannelID string `json:"channel_id,omitempty"`
	Engine    string `json:"engine,omitempty"`
	PostID    string `json:"post_id"`
}

// DeleteData reports whether the deleted post was a thread root, so the CLI
// can warn about Mattermost's cascade: deleting a root soft-deletes every
// reply underneath it, including replies from other users, in the same
// request the root itself was deleted in. It is the single exception to
// "delete carries no data": unlike an echo of the post id, this is not
// something the caller already knows — RootId comes back from GetPost, which
// only the engine calls.
type DeleteData struct {
	ThreadRoot bool `json:"thread_root,omitempty"`
}

type DeletePreviewArgs struct {
	ChannelID          string    `json:"channel_id,omitempty"`
	Channel            string    `json:"channel,omitempty"`
	Engine             string    `json:"engine,omitempty"`
	PostIDs            []string  `json:"post_ids"`
	From               time.Time `json:"from,omitempty"`
	To                 time.Time `json:"to,omitempty"`
	IncludeThreadRoots bool      `json:"include_thread_roots,omitempty"`
}

// DeleteMaxPostIDs bounds destructive preview and batch requests at the
// protocol boundary. Clients may still send smaller chunks as needed.
const DeleteMaxPostIDs = 1000

type DeleteRangeSummaryArgs struct {
	Channel            string    `json:"channel"`
	From               time.Time `json:"from"`
	To                 time.Time `json:"to"`
	IncludeThreadRoots bool      `json:"include_thread_roots,omitempty"`
}

type DeleteRangeChunkArgs struct {
	Channel            string    `json:"channel"`
	From               time.Time `json:"from"`
	To                 time.Time `json:"to"`
	Cursor             string    `json:"cursor,omitempty"`
	Limit              int       `json:"limit"`
	IncludeThreadRoots bool      `json:"include_thread_roots,omitempty"`
	ProtectedRootIDs   []string  `json:"protected_root_ids,omitempty"`
}

type DeleteRangeSummaryData struct {
	ChannelID            string   `json:"channel_id"`
	TeamID               string   `json:"team_id"`
	Requested            int      `json:"requested"`
	Effective            int      `json:"effective"`
	SkippedRoots         int      `json:"skipped_roots"`
	ProtectedRoots       int      `json:"protected_roots"`
	ProtectedRootIDs     []string `json:"protected_root_ids,omitempty"`
	IncludeThreadRoots   bool     `json:"include_thread_roots"`
	MinTimestamp         int64    `json:"min_timestamp,omitempty"`
	MaxTimestamp         int64    `json:"max_timestamp,omitempty"`
	RequiresElevatedAuth bool     `json:"requires_elevated_auth"`
}

// DeleteJobState is persisted by uni-chatd; values are part of the wire API.
type DeleteJobState string

const (
	DeleteJobQueued              DeleteJobState = "queued"
	DeleteJobRunning             DeleteJobState = "running"
	DeleteJobPaused              DeleteJobState = "paused"
	DeleteJobNeedsReconciliation DeleteJobState = "needs_reconciliation"
	DeleteJobCompleted           DeleteJobState = "completed"
	DeleteJobCancelled           DeleteJobState = "cancelled"
	DeleteJobFailed              DeleteJobState = "failed"
	DeleteJobExpired             DeleteJobState = "expired"
)

func (s DeleteJobState) Valid() bool {
	switch s {
	case DeleteJobQueued, DeleteJobRunning, DeleteJobPaused, DeleteJobNeedsReconciliation, DeleteJobCompleted, DeleteJobCancelled, DeleteJobFailed, DeleteJobExpired:
		return true
	default:
		return false
	}
}

type DeleteJobScope struct {
	Engine             string    `json:"engine"`
	Channel            string    `json:"channel"`
	ResolvedChannelID  string    `json:"resolved_channel_id,omitempty"`
	From               time.Time `json:"from"`
	To                 time.Time `json:"to"`
	BatchSize          int       `json:"batch_size"`
	IncludeThreadRoots bool      `json:"include_thread_roots,omitempty"`
}

type DeleteJobStartArgs struct {
	Scope     DeleteJobScope `json:"scope"`
	RequestID string         `json:"request_id"`
}

type DeleteJobStartData struct {
	JobID  string          `json:"job_id"`
	State  DeleteJobState  `json:"state"`
	Status DeleteJobStatus `json:"status"`
	Reused bool            `json:"reused,omitempty"`
}

type DeleteJobIDArgs struct {
	JobID string `json:"job_id"`
}

type DeleteJobStatus struct {
	JobID      string             `json:"job_id"`
	State      DeleteJobState     `json:"state"`
	Scope      DeleteJobScope     `json:"scope"`
	Requested  int                `json:"requested"`
	Effective  int                `json:"effective"`
	Deleted    int                `json:"deleted"`
	Skipped    int                `json:"skipped"`
	Protected  int                `json:"protected"`
	Errors     []DeleteBatchError `json:"errors,omitempty"`
	Remaining  int                `json:"remaining"`
	CreatedAt  time.Time          `json:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
	LeaseUntil time.Time          `json:"lease_until"`
	LastError  string             `json:"last_error,omitempty"`
}

type DeleteRangeChunkData struct {
	ChannelID            string         `json:"channel_id"`
	Requested            int            `json:"requested"`
	Effective            int            `json:"effective"`
	SkippedRoots         int            `json:"skipped_roots"`
	ProtectedRoots       int            `json:"protected_roots"`
	IncludeThreadRoots   bool           `json:"include_thread_roots"`
	Targets              []DeleteTarget `json:"targets"`
	NextCursor           string         `json:"next_cursor,omitempty"`
	Complete             bool           `json:"complete"`
	RequiresElevatedAuth bool           `json:"requires_elevated_auth"`
	Snapshot             string         `json:"snapshot"`
}

type deleteArgsWire struct {
	ChannelID          string     `json:"channel_id,omitempty"`
	Channel            string     `json:"channel,omitempty"`
	Engine             string     `json:"engine,omitempty"`
	PostIDs            []string   `json:"post_ids"`
	From               *time.Time `json:"from,omitempty"`
	To                 *time.Time `json:"to,omitempty"`
	Snapshot           string     `json:"snapshot,omitempty"`
	IncludeThreadRoots bool       `json:"include_thread_roots,omitempty"`
	ProtectedRootIDs   []string   `json:"protected_root_ids,omitempty"`
	RangeChunk         bool       `json:"range_chunk,omitempty"`
}

func (a DeletePreviewArgs) MarshalJSON() ([]byte, error) {
	return json.Marshal(deleteArgsWire{ChannelID: a.ChannelID, Channel: a.Channel, Engine: a.Engine, PostIDs: a.PostIDs, From: optionalTime(a.From), To: optionalTime(a.To), IncludeThreadRoots: a.IncludeThreadRoots})
}

func (a *DeletePreviewArgs) UnmarshalJSON(data []byte) error {
	var w deleteArgsWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	a.ChannelID, a.Channel, a.Engine, a.PostIDs, a.IncludeThreadRoots = w.ChannelID, w.Channel, w.Engine, w.PostIDs, w.IncludeThreadRoots
	a.From, a.To = zeroTime(w.From), zeroTime(w.To)
	return nil
}

type DeleteTarget struct {
	PostID    string `json:"post_id"`
	ChannelID string `json:"channel_id"`
	AuthorID  string `json:"author_id"`
	Own       bool   `json:"own"`
	RootID    string `json:"root_id,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

type DeletePreviewData struct {
	ChannelID            string         `json:"channel_id"`
	Requested            int            `json:"requested"`
	MinTimestamp         int64          `json:"min_timestamp,omitempty"`
	MaxTimestamp         int64          `json:"max_timestamp,omitempty"`
	MinPostID            string         `json:"min_post_id,omitempty"`
	MaxPostID            string         `json:"max_post_id,omitempty"`
	Targets              []DeleteTarget `json:"targets"`
	RequiresElevatedAuth bool           `json:"requires_elevated_auth"`
	SkippedRootIDs       []string       `json:"skipped_root_ids,omitempty"`
	Snapshot             string         `json:"snapshot"`
}

type DeleteBatchArgs struct {
	ChannelID          string    `json:"channel_id,omitempty"`
	Engine             string    `json:"engine,omitempty"`
	PostIDs            []string  `json:"post_ids"`
	From               time.Time `json:"from,omitempty"`
	To                 time.Time `json:"to,omitempty"`
	Snapshot           string    `json:"snapshot"`
	IncludeThreadRoots bool      `json:"include_thread_roots,omitempty"`
	ProtectedRootIDs   []string  `json:"protected_root_ids,omitempty"`
	RangeChunk         bool      `json:"range_chunk,omitempty"`
}

func (a DeleteBatchArgs) MarshalJSON() ([]byte, error) {
	return json.Marshal(deleteArgsWire{ChannelID: a.ChannelID, Engine: a.Engine, PostIDs: a.PostIDs, From: optionalTime(a.From), To: optionalTime(a.To), Snapshot: a.Snapshot, IncludeThreadRoots: a.IncludeThreadRoots, ProtectedRootIDs: a.ProtectedRootIDs, RangeChunk: a.RangeChunk})
}

func (a *DeleteBatchArgs) UnmarshalJSON(data []byte) error {
	var w deleteArgsWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	a.ChannelID, a.Engine, a.PostIDs, a.Snapshot, a.IncludeThreadRoots, a.ProtectedRootIDs, a.RangeChunk = w.ChannelID, w.Engine, w.PostIDs, w.Snapshot, w.IncludeThreadRoots, w.ProtectedRootIDs, w.RangeChunk
	a.From, a.To = zeroTime(w.From), zeroTime(w.To)
	return nil
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func zeroTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

type DeleteBatchError struct {
	PostID string `json:"post_id"`
	Error  string `json:"error"`
}

type DeleteBatchData struct {
	Requested   int                `json:"requested"`
	Deleted     int                `json:"deleted"`
	Partial     bool               `json:"partial"`
	Complete    bool               `json:"complete"`
	Errors      []DeleteBatchError `json:"errors,omitempty"`
	Remaining   []string           `json:"remaining,omitempty"`
	ThreadRoots []string           `json:"thread_roots,omitempty"`
	Cascaded    []string           `json:"cascaded,omitempty"`
	Snapshot    string             `json:"snapshot"`
}

// StatusData backs the "status" command.
type StatusData struct {
	Running             bool   `json:"running"`
	LastCheck           string `json:"last_check,omitempty"` // RFC3339, empty = never
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
}

type PollIntervalArgs struct {
	Seconds int `json:"seconds"`
}

// MarshalArgs JSON-encodes a payload for Request.Args / Response.Data.
func MarshalArgs(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// UnmarshalJSONObject decodes a response payload whose contract requires a
// JSON object. An explicit empty object is valid; absent data, null, arrays,
// and malformed JSON are not.
func UnmarshalJSONObject(data []byte, out any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return errors.New("response data is empty: want JSON object")
	}
	if !json.Valid(trimmed) {
		return errors.New("response data is malformed JSON: want JSON object")
	}
	if trimmed[0] != '{' {
		return errors.New("response data is not a JSON object")
	}
	return json.Unmarshal(trimmed, out)
}

// OK builds a successful response carrying data (nil data => bare {ok:true}).
func OK(data any) Response {
	if data == nil {
		return Response{OK: true}
	}
	return Response{OK: true, Data: MarshalArgs(data)}
}

// Fail builds an {ok:false} response from an error.
func Fail(err error) Response { return Response{OK: false, Error: err.Error()} }

// ErrDaemonUnreachable means dialing the socket failed — the daemon is not
// running. The CLI turns it into a "brew services start uni-chat" hint instead
// of hanging.
var ErrDaemonUnreachable = errors.New("uni-chatd is not running")

// ErrServerBusy is returned as a normal failed response when all connection
// slots are occupied. Keeping this in the response preserves the wire contract.
var ErrServerBusy = errors.New("server busy")

// Call dials the socket, sends one request, reads one response. A dial failure
// (no listener) is returned wrapped in ErrDaemonUnreachable — fast, never a
// hang. timeout bounds the response read (generous: check may retry upstream).
func Call(socket string, req Request, timeout time.Duration) (Response, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrDaemonUnreachable, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// Listen creates the 0700 socket dir, clears any stale socket, listens, and
// chmods the socket 0600 — the single-user filesystem boundary.
func Listen(socket string) (net.Listener, error) {
	dir := filepath.Dir(socket)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- the directory must remain private to the user.
		return nil, err
	}
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// Handler turns one request into one response.
type Handler func(Request) Response

// Serve accepts connections until the listener is closed, one goroutine each.
// A transient Accept error (e.g. a client aborting mid-handshake, or the
// process briefly running out of file descriptors) does not stop the loop —
// only a closed listener does. That keeps a daemon restart the sole recovery
// path for a truly dead listener, instead of a stray error silently ending
// the accept loop while the process stays alive and serves nothing.
func Serve(ln net.Listener, h Handler) {
	ServeContext(context.Background(), ln, h)
}

// ServeContext accepts connections until the listener closes or ctx is
// cancelled. Cancellation closes active sockets and gives handlers a bounded
// opportunity to finish persistence before Serve returns.
func ServeContext(ctx context.Context, ln net.Listener, h Handler) {
	sem := make(chan struct{}, serveMaxConcurrentConnections)
	var wg sync.WaitGroup
	var activeMu sync.Mutex
	active := map[net.Conn]struct{}{}
	shutdown := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = ln.Close()
		case <-shutdown:
		}
	}()
	defer func() {
		close(shutdown)
		activeMu.Lock()
		for conn := range active {
			_ = conn.Close()
		}
		activeMu.Unlock()
		wait := make(chan struct{})
		go func() { wg.Wait(); close(wait) }()
		select {
		case <-wait:
		case <-time.After(serveShutdownTimeout):
		}
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return // listener closed (shutdown)
			}
			time.Sleep(5 * time.Millisecond) // avoid a tight spin on a persistent error
			continue
		}
		select {
		case sem <- struct{}{}:
			activeMu.Lock()
			active[conn] = struct{}{}
			activeMu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				defer func() {
					activeMu.Lock()
					delete(active, conn)
					activeMu.Unlock()
				}()
				serveConn(conn, h)
			}()
		default:
			go writeBusyResponse(conn)
		}
	}
}

const (
	serveMaxConcurrentConnections  = 32
	maxRequestJSONBytes            = 1 << 20
	serveConnectionTimeout         = 120 * time.Second
	serveShutdownTimeout           = 5 * time.Second
	serveBusyWriteTimeout          = 100 * time.Millisecond
	SyncServeTimeout               = 11 * time.Minute
	deleteRangeSummaryServeTimeout = 11 * time.Minute
	DeleteJobStartTimeout          = 12 * time.Minute
	deleteJobStartServeTimeout     = DeleteJobStartTimeout
)

func serveConnectionTimeoutFor(cmd string) time.Duration {
	if cmd == "delete_job_start" {
		return deleteJobStartServeTimeout
	}
	if cmd == "delete_range_summary" {
		return deleteRangeSummaryServeTimeout
	}
	if cmd == "sync" {
		return SyncServeTimeout
	}
	return serveConnectionTimeout
}

func writeBusyResponse(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetWriteDeadline(time.Now().Add(serveBusyWriteTimeout))
	_ = json.NewEncoder(conn).Encode(Fail(ErrServerBusy))
}

func serveConn(conn net.Conn, h Handler) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(serveConnectionTimeout))
	var req Request
	if err := decodeBoundedRequest(conn, &req); err != nil {
		_ = json.NewEncoder(conn).Encode(Fail(errors.New("bad request")))
		return
	}
	_ = conn.SetDeadline(time.Now().Add(serveConnectionTimeoutFor(req.Cmd)))
	_ = json.NewEncoder(conn).Encode(callHandler(h, req))
}

func decodeBoundedRequest(in io.Reader, req *Request) error {
	limited := &io.LimitedReader{R: in, N: maxRequestJSONBytes + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(req); err != nil {
		if limited.N == 0 {
			return fmt.Errorf("request JSON exceeds %d bytes", maxRequestJSONBytes)
		}
		return err
	}
	if decoder.InputOffset() > maxRequestJSONBytes {
		return fmt.Errorf("request JSON exceeds %d bytes", maxRequestJSONBytes)
	}
	trailing := decoder.Buffered()
	if hasNonWhitespace(trailing) {
		return fmt.Errorf("request JSON exceeds %d bytes or contains trailing data", maxRequestJSONBytes)
	}
	if conn, ok := in.(net.Conn); ok {
		return rejectSocketTrailingData(conn)
	}
	remaining, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if hasNonWhitespace(bytes.NewReader(remaining)) {
		return fmt.Errorf("request JSON exceeds %d bytes or contains trailing data", maxRequestJSONBytes)
	}
	return nil
}

func hasNonWhitespace(in io.Reader) bool {
	data, err := io.ReadAll(in)
	return err == nil && len(bytes.TrimSpace(data)) != 0
}

func rejectSocketTrailingData(conn net.Conn) error {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	var one [1]byte
	for {
		n, err := conn.Read(one[:])
		if n > 0 && !bytes.Contains([]byte(" \t\r\n"), one[:1]) {
			return fmt.Errorf("request JSON exceeds %d bytes or contains trailing data", maxRequestJSONBytes)
		}
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return nil
			}
			return err
		}
	}
}

// callHandler runs h(req), converting a panic into a Fail response instead of
// letting it unwind out of the per-connection goroutine and crash the
// process. A single misbehaving handler (nil map, failed type assertion, …)
// then costs one connection, not every in-flight one.
func callHandler(h Handler, req Request) (resp Response) {
	defer func() {
		if r := recover(); r != nil {
			resp = Fail(fmt.Errorf("internal error: %v", r))
		}
	}()
	return h(req)
}

// EnginesPath is ~/.uni-chat/engines.json — the engine registry the router
// reads on every request. Overridable via UNI_CHAT_DIR (through Dir).
func EnginesPath() string { return filepath.Join(Dir(), "engines.json") }

// EngineDir is ~/.uni-chat/engines/<name> — one adapter's own config.json and
// state.json. Only that adapter reads or writes it.
func EngineDir(name string) string { return filepath.Join(Dir(), "engines", name) }

// EngineStatusData is an adapter's "status" reply: when it last checked and
// whether it is configured. The router aggregates these into the CLI-facing
// StatusData without knowing any adapter's state.json format.
type EngineStatusData struct {
	LastCheck  string `json:"last_check,omitempty"` // RFC3339, empty = never
	Configured bool   `json:"configured"`
}

type CapabilitiesArgs struct {
	Refresh bool `json:"refresh,omitempty"`
}

type CapabilityReason struct {
	Code string `json:"code"`
}

type CapabilityInfo struct {
	ID     string            `json:"id"`
	Label  string            `json:"label"`
	Action string            `json:"action"`
	Status string            `json:"status"`
	Reason *CapabilityReason `json:"reason,omitempty"`
}

type EngineCapabilitiesData struct {
	Engine       string           `json:"engine"`
	Capabilities []CapabilityInfo `json:"capabilities"`
}

type CapabilitiesData struct {
	Engines []EngineCapabilitiesData `json:"engines"`
	Partial bool                     `json:"partial,omitempty"`
	Errors  []SyncError              `json:"errors,omitempty"`
}

// ServeStdio is the adapter side of the machine-call transport: it reads
// exactly one Request from in, runs h (panic-guarded via callHandler), and
// writes exactly one Response to out — one request, one response, then the
// process exits (spawn-per-request). A malformed request still yields an
// {ok:false} Response rather than a silent failure.
func ServeStdio(in io.Reader, out io.Writer, h Handler) error {
	var req Request
	if err := decodeBoundedRequest(in, &req); err != nil {
		return json.NewEncoder(out).Encode(Fail(fmt.Errorf("bad request: %w", err)))
	}
	return json.NewEncoder(out).Encode(callHandler(h, req))
}

// CallStdio is the router side of the machine-call transport: it spawns bin
// with args, writes req to its stdin as one JSON value, reads one JSON Response
// from its stdout, and waits for it to exit (bounded by timeout). A spawn/exit/
// decode failure is returned as an error — the router turns it into
// skip-and-warn (check) or a Fail (post/watch); a logical adapter failure comes
// back inside the Response with OK=false and never as an error here.
func CallStdio(bin string, args []string, req Request, timeout time.Duration) (Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	cmd := exec.Command(bin, args...) // #nosec G204 -- the engine binary is selected from the private registry.
	cmd.Stdin = bytes.NewReader(reqBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	configureProcessGroup(cmd)
	if err := runBoundedCommand(ctx, cmd, timeout); err != nil {
		return Response{}, fmt.Errorf("engine %s: %w: %s", bin, err, strings.TrimSpace(stderr.String()))
	}
	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		return Response{}, fmt.Errorf("engine %s: bad response: %w", bin, err)
	}
	return resp, nil
}
