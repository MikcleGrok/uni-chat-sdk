package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// uniqueIDCounter backs uniqueID: an atomic counter (rather than e.g.
// time.Now().UnixNano() alone) so two tests started in the same nanosecond
// under t.Parallel() can never collide.
var uniqueIDCounter int64

// uniqueID returns an identifier unique to this one test invocation — this
// process's pid plus a monotonically increasing counter — so a single value
// can anchor every resource a parallel acceptance test creates: a socket
// directory name (shortSocketDir below) and, for the CallStdio helper-process
// tests, the argv marker that replaces the old shared-env-var mechanism (see
// engineHelperMarkerFlag). 12-test-contract.md:257 requires exactly this:
// "MUST изолироваться уникальным маркером <...>, а не общим состоянием
// стенда."
func uniqueID(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("uc-%d-%d", os.Getpid(), atomic.AddInt64(&uniqueIDCounter, 1))
}

// shortSocketDir returns a short-lived temp dir, independent of the test
// name's length. Unlike t.TempDir() (which embeds the full subtest name),
// this keeps the resulting socket path safely under the ~104-byte sockaddr_un
// limit macOS enforces — a long test name plus t.TempDir() can otherwise
// produce a path that fails Listen with "bind: invalid argument".
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", uniqueID(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestCallRoundTrip(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Errorf("close listener: %v", err)
		}
	}()
	go Serve(ln, func(req Request) Response {
		if req.Cmd != "ping" {
			return Fail(errors.New("bad cmd"))
		}
		return OK(StatusData{Running: true, LastCheck: "yesterday"})
	})

	resp, err := Call(sock, Request{Cmd: "ping"}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("resp.OK = false, error = %q", resp.Error)
	}
	var sd StatusData
	if err := json.Unmarshal(resp.Data, &sd); err != nil {
		t.Fatal(err)
	}
	if !sd.Running || sd.LastCheck != "yesterday" {
		t.Fatalf("decoded StatusData = %+v", sd)
	}
}

func TestUnmarshalJSONObjectSemantics(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{name: "empty object", data: []byte(`{}`)},
		{name: "whitespace object", data: []byte(" \n{} \t")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]any
			if err := UnmarshalJSONObject(tc.data, &got); err != nil {
				t.Fatalf("error = %v, want valid object", err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{name: "absent", data: nil, want: "empty"},
		{name: "null", data: []byte("null"), want: "not a JSON object"},
		{name: "array", data: []byte("[]"), want: "not a JSON object"},
		{name: "malformed", data: []byte("{"), want: "malformed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := UnmarshalJSONObject(tc.data, &map[string]any{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDeleteJobProtocolRoundTripAndStateValidation(t *testing.T) {
	from := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	want := DeleteJobStartArgs{Scope: DeleteJobScope{Engine: "mattermost", Channel: "team/channel", From: from, To: from.Add(time.Hour), BatchSize: 25, IncludeThreadRoots: true}, RequestID: "range:abc"}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got DeleteJobStartArgs
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	for _, state := range []DeleteJobState{DeleteJobQueued, DeleteJobRunning, DeleteJobPaused, DeleteJobNeedsReconciliation, DeleteJobCompleted, DeleteJobCancelled, DeleteJobFailed, DeleteJobExpired} {
		if !state.Valid() {
			t.Fatalf("stable state %q was rejected", state)
		}
	}
	if DeleteJobState("made-up").Valid() {
		t.Fatal("unknown state was accepted")
	}
	var status DeleteJobStatus
	if err := json.Unmarshal([]byte(`{"state":"made-up"}`), &status); err != nil {
		t.Fatal(err)
	}
	if status.State.Valid() {
		t.Fatal("unknown wire state was accepted")
	}
}

func TestDeleteJobWireTypesRoundTrip(t *testing.T) {
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	args := DeleteJobIDArgs{JobID: "job-1"}
	start := DeleteJobStartData{JobID: "job-1", State: DeleteJobQueued, Status: DeleteJobStatus{JobID: "job-1", State: DeleteJobQueued, Scope: DeleteJobScope{Engine: "mattermost", Channel: "c", From: now, To: now.Add(time.Hour), BatchSize: 3}}, Reused: true}
	status := DeleteJobStatus{JobID: "job-1", State: DeleteJobFailed, LastError: "boom", UpdatedAt: now}
	for _, value := range []any{args, start, status} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		switch value.(type) {
		case DeleteJobIDArgs:
			decoded = &DeleteJobIDArgs{}
		case DeleteJobStartData:
			decoded = &DeleteJobStartData{}
		case DeleteJobStatus:
			decoded = &DeleteJobStatus{}
		}
		if err := json.Unmarshal(raw, decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decodedValue(decoded), value) {
			t.Fatalf("wire value=%+v decoded=%+v", value, decoded)
		}
	}
	resp := Fail(errors.New("cancel failed"))
	resp.Data = MarshalArgs(status)
	var attached DeleteJobStatus
	if err := json.Unmarshal(resp.Data, &attached); err != nil || attached.JobID != "job-1" {
		t.Fatalf("attached cancel data=%+v err=%v", attached, err)
	}
}

func decodedValue(value any) any {
	switch value := value.(type) {
	case *DeleteJobIDArgs:
		return *value
	case *DeleteJobStartData:
		return *value
	case *DeleteJobStatus:
		return *value
	default:
		return nil
	}
}

func TestCallDaemonUnreachable(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(t.TempDir(), "missing.sock")
	_, err := Call(sock, Request{Cmd: "ping"}, time.Second)
	if !errors.Is(err, ErrDaemonUnreachable) {
		t.Fatalf("err = %v, want ErrDaemonUnreachable", err)
	}
}

func TestDeleteRangeProtocolJSONRoundTrip(t *testing.T) {
	from := time.Date(1970, 1, 1, 0, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	to := time.Date(2026, 8, 3, 0, 0, 0, 0, from.Location())
	args := DeleteRangeChunkArgs{Channel: "mattermost/devteam/qa-alerts", From: from, To: to, Cursor: "cursor-token", Limit: 250, IncludeThreadRoots: true, ProtectedRootIDs: []string{"root-recent"}}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DeleteRangeChunkArgs
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Channel != args.Channel || !decoded.From.Equal(from) || !decoded.To.Equal(to) || decoded.Cursor != args.Cursor || decoded.Limit != args.Limit || decoded.IncludeThreadRoots != args.IncludeThreadRoots || !reflect.DeepEqual(decoded.ProtectedRootIDs, args.ProtectedRootIDs) {
		t.Fatalf("decoded args = %+v, want %+v", decoded, args)
	}
	data := DeleteRangeSummaryData{ChannelID: "c", TeamID: "t", Requested: 114478, Effective: 114000, SkippedRoots: 478, ProtectedRoots: 1, ProtectedRootIDs: []string{"root-recent"}, IncludeThreadRoots: true, RequiresElevatedAuth: true}
	raw, err = json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var decodedData DeleteRangeSummaryData
	if err := json.Unmarshal(raw, &decodedData); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodedData, data) {
		t.Fatalf("decoded summary = %+v, want %+v", decodedData, data)
	}
}

func TestSearchWireTypesRoundTrip(t *testing.T) {
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	want := SearchData{
		Items:   []SearchItem{{PostID: "p1", ChannelID: "c1", ChannelRef: "team/dev", Sender: "Alice", SenderUserID: "u1", Message: "timezone", CreatedAt: from.Add(time.Hour), ThreadRootID: "root", Link: "https://mm/team/pl/p1", Reactions: []string{"+1"}}},
		Partial: true,
		HasMore: true,
		Errors:  []SearchError{{Engine: "mattermost", TeamID: "t2", Error: "unavailable"}},
	}
	raw, err := json.Marshal(SearchArgs{Query: "timezone", Channel: "mattermost/team/dev", Author: "Alice", SinceTime: from, UntilTime: to})
	if err != nil {
		t.Fatal(err)
	}
	var args SearchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatal(err)
	}
	if args.Author != "Alice" || args.Query != "timezone" || args.Channel != "mattermost/team/dev" || !args.SinceTime.Equal(from) || !args.UntilTime.Equal(to) {
		t.Fatalf("args = %+v", args)
	}
	raw, err = json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got SearchData
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("search round trip = %+v, want %+v", got, want)
	}
}

// TestCallHandlerPanicRecovered proves a panicking Handler cannot crash the
// process: serveConn must recover it and reply with an {ok:false} Response
// carrying a non-empty Error, and the daemon must stay up to serve the next
// request on the same listener.
func TestCallHandlerPanicRecovered(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortSocketDir(t), "d.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Errorf("close listener: %v", err)
		}
	}()
	go Serve(ln, func(req Request) Response {
		if req.Cmd == "panic" {
			panic("boom")
		}
		return OK(nil)
	})

	resp, err := Call(sock, Request{Cmd: "panic"}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK {
		t.Fatalf("resp.OK = true, want false after a recovered handler panic")
	}
	if resp.Error == "" {
		t.Fatal("resp.Error is empty, want a non-empty message describing the recovered panic")
	}

	// The listener/accept loop must still be alive: a follow-up call on the
	// same socket should succeed normally.
	resp2, err := Call(sock, Request{Cmd: "ping"}, 5*time.Second)
	if err != nil {
		t.Fatalf("daemon did not survive the panic: %v", err)
	}
	if !resp2.OK {
		t.Fatalf("resp2.OK = false after recovery, error = %q", resp2.Error)
	}
}

// flakyListener wraps a real net.Listener and fails the first Accept call
// with a transient (non-ErrClosed) error, then delegates every subsequent
// call to the real listener. It lets TestServeSurvivesTransientAcceptError
// deterministically exercise Serve's "keep looping on a non-fatal Accept
// error" path without depending on OS-level fault injection.
type flakyListener struct {
	net.Listener
	failed int32 // atomic; 0 = not yet failed once, 1 = the one failure has fired
}

func (l *flakyListener) Accept() (net.Conn, error) {
	if atomic.CompareAndSwapInt32(&l.failed, 0, 1) {
		return nil, &net.OpError{Op: "accept", Net: "unix", Err: errors.New("transient accept error (simulated ECONNABORTED)")}
	}
	return l.Listener.Accept()
}

// TestServeSurvivesTransientAcceptError proves Serve does not stop accepting
// after a non-ErrClosed error from ln.Accept(): the loop must keep going and
// still accept the next real connection.
func TestServeSurvivesTransientAcceptError(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortSocketDir(t), "d.sock")
	realLn, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ln := &flakyListener{Listener: realLn}
	defer func() {
		if err := ln.Close(); err != nil {
			t.Errorf("close listener: %v", err)
		}
	}()
	go Serve(ln, func(req Request) Response {
		return OK(StatusData{Running: true})
	})

	resp, err := Call(sock, Request{Cmd: "ping"}, 5*time.Second)
	if err != nil {
		t.Fatalf("Serve did not survive the transient Accept error: %v", err)
	}
	if !resp.OK {
		t.Fatalf("resp.OK = false, error = %q", resp.Error)
	}
	if atomic.LoadInt32(&ln.failed) != 1 {
		t.Fatal("flakyListener never hit its injected failure — test did not exercise the transient-error path")
	}
}

func TestServeReturnsBusyWhenConnectionLimitIsReached(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortSocketDir(t), "d.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	entered := make(chan struct{}, serveMaxConcurrentConnections)
	release := make(chan struct{})
	go Serve(ln, func(req Request) Response {
		entered <- struct{}{}
		<-release
		return OK(nil)
	})
	var wg sync.WaitGroup
	for i := 0; i < serveMaxConcurrentConnections; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = Call(sock, Request{Cmd: "hold"}, 5*time.Second)
		}()
	}
	for i := 0; i < serveMaxConcurrentConnections; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("connection slot was not occupied")
		}
	}
	resp, err := Call(sock, Request{Cmd: "busy"}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error != ErrServerBusy.Error() {
		t.Fatalf("busy response = %+v, want failed server busy response", resp)
	}
	close(release)
	wg.Wait()
}

// TestServeBusyManySimultaneousCallersAllGetGracefulResponse stresses the
// same race TestServeReturnsBusyWhenConnectionLimitIsReached exercises with a
// single extra caller, but with many simultaneous ones, to make a rare
// ordering bug reproduce reliably instead of only occasionally under full
// suite load. writeBusyResponse used to write its response and Close() the
// connection without ever reading anything the caller sent — if the caller's
// own Call() hadn't finished writing its request by the time that Close()
// landed, the write failed with a raw broken-pipe/connection-reset error
// instead of the caller ever seeing the graceful {ok:false} busy response.
func TestServeBusyManySimultaneousCallersAllGetGracefulResponse(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortSocketDir(t), "d.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	entered := make(chan struct{}, serveMaxConcurrentConnections)
	release := make(chan struct{})
	go Serve(ln, func(Request) Response { entered <- struct{}{}; <-release; return OK(nil) })
	var holders sync.WaitGroup
	for i := 0; i < serveMaxConcurrentConnections; i++ {
		holders.Add(1)
		go func() {
			defer holders.Done()
			_, _ = Call(sock, Request{Cmd: "hold"}, 5*time.Second)
		}()
	}
	for i := 0; i < serveMaxConcurrentConnections; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("connection slot was not occupied")
		}
	}

	const extraCallers = 20
	type outcome struct {
		resp Response
		err  error
	}
	results := make(chan outcome, extraCallers)
	var extras sync.WaitGroup
	for i := 0; i < extraCallers; i++ {
		extras.Add(1)
		go func() {
			defer extras.Done()
			resp, err := Call(sock, Request{Cmd: "busy"}, 5*time.Second)
			results <- outcome{resp, err}
		}()
	}
	extras.Wait()
	close(results)
	for r := range results {
		if r.err != nil {
			t.Fatalf("busy caller got a raw I/O error instead of a graceful response: %v", r.err)
		}
		if r.resp.OK || r.resp.Error != ErrServerBusy.Error() {
			t.Fatalf("busy response = %+v, want failed server busy response", r.resp)
		}
	}

	close(release)
	holders.Wait()
}

func TestServeBusyUnreadClientDoesNotBlockAcceptLoop(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortSocketDir(t), "d.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	entered := make(chan struct{}, serveMaxConcurrentConnections)
	release := make(chan struct{})
	go Serve(ln, func(Request) Response { entered <- struct{}{}; <-release; return OK(nil) })
	clients := make([]net.Conn, 0, serveMaxConcurrentConnections)
	for i := 0; i < serveMaxConcurrentConnections; i++ {
		conn, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, conn)
		if _, err := conn.Write([]byte(`{"cmd":"hold"}` + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < serveMaxConcurrentConnections; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("connection slot was not occupied")
		}
	}
	busy, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	if _, err := busy.Write([]byte(`{"cmd":"busy"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	next, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		t.Fatalf("accept loop blocked behind unread busy client: %v", err)
	}
	_ = next.Close()
	close(release)
	for _, conn := range clients {
		_ = conn.Close()
	}
}

func TestServeContextReturnsAfterShutdownDeadline(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortSocketDir(t), "d.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ServeContext(ctx, ln, func(context.Context, Request) Response {
			close(started)
			time.Sleep(serveShutdownTimeout * 2)
			return OK(nil)
		})
		close(done)
	}()
	go func() { _, _ = Call(sock, Request{Cmd: "slow"}, time.Second) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(serveShutdownTimeout + time.Second):
		t.Fatal("Serve did not return after shutdown deadline")
	}
}

type trackingListener struct {
	net.Listener
	accepted *atomic.Int32
	closed   *atomic.Int32
	signal   chan struct{}
}

func (l *trackingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.accepted.Add(1)
	return &trackingConn{Conn: conn, closed: l.closed, signal: l.signal}, nil
}

type trackingConn struct {
	net.Conn
	closed *atomic.Int32
	signal chan struct{}
	once   sync.Once
}

func (c *trackingConn) Close() error {
	c.once.Do(func() {
		c.closed.Add(1)
		if c.signal != nil {
			close(c.signal)
		}
	})
	return c.Conn.Close()
}

func TestServeContextRepeatedLifecycleClosesEveryAcceptedConnection(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortSocketDir(t), "d.sock")
	const cycles = 8
	var accepted, closed atomic.Int32
	for i := 0; i < cycles; i++ {
		realLn, err := Listen(sock)
		if err != nil {
			t.Fatal(err)
		}
		ln := &trackingListener{Listener: realLn, accepted: &accepted, closed: &closed}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			ServeContext(ctx, ln, func(context.Context, Request) Response { return OK(nil) })
			close(done)
		}()
		if _, err := Call(sock, Request{Cmd: "ping"}, time.Second); err != nil {
			cancel()
			_ = ln.Close()
			t.Fatal(err)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			_ = ln.Close()
			t.Fatalf("lifecycle %d did not shut down", i)
		}
	}
	if accepted.Load() != cycles {
		t.Fatalf("accepted connections = %d, want %d", accepted.Load(), cycles)
	}
	if closed.Load() != accepted.Load() {
		t.Fatalf("closed server connections = %d, want %d", closed.Load(), accepted.Load())
	}
}

func TestServeContextCancellationClosesActiveConnection(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortSocketDir(t), "d.sock")
	realLn, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	var closed atomic.Int32
	closedSignal := make(chan struct{})
	ln := &trackingListener{Listener: realLn, accepted: new(atomic.Int32), closed: &closed, signal: closedSignal}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		ServeContext(ctx, ln, func(context.Context, Request) Response {
			close(started)
			<-release
			return OK(nil)
		})
		close(done)
	}()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		_ = ln.Close()
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(Request{Cmd: "hold"}); err != nil {
		_ = conn.Close()
		_ = ln.Close()
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		_ = conn.Close()
		_ = ln.Close()
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case <-closedSignal:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not close the active connection")
	}
	close(release)
	_ = conn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ServeContext did not finish after handler release")
	}
}

// TestServeContextHandlerObservesConnectionContextCancellation proves the
// actual point of HandlerContext: unlike a plain Handler (which has no way
// to receive a context at all, so it necessarily keeps running in the
// background after ServeContext returns — the bug this type exists to fix),
// a HandlerContext that selects on ctx.Done() returns as soon as
// ServeContext's own ctx is cancelled, well before serveShutdownTimeout —
// which only bounds handlers that do NOT observe cancellation, as
// TestServeContextReturnsAfterShutdownDeadline above still proves for the
// Handler-via-WithContext compatibility path.
func TestServeContextHandlerObservesConnectionContextCancellation(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortSocketDir(t), "d.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	observedCancel := make(chan struct{})
	done := make(chan struct{})
	go func() {
		ServeContext(ctx, ln, func(connCtx context.Context, req Request) Response {
			close(started)
			<-connCtx.Done()
			close(observedCancel)
			return OK(nil)
		})
		close(done)
	}()
	go func() { _, _ = Call(sock, Request{Cmd: "hold"}, 5*time.Second) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}

	cancelledAt := time.Now()
	cancel()

	select {
	case <-observedCancel:
	case <-time.After(serveShutdownTimeout):
		t.Fatal("handler did not observe connection context cancellation before serveShutdownTimeout")
	}
	if elapsed := time.Since(cancelledAt); elapsed >= serveShutdownTimeout {
		t.Fatalf("handler observed cancellation after %v, want well before serveShutdownTimeout (%v)", elapsed, serveShutdownTimeout)
	}

	select {
	case <-done:
	case <-time.After(serveShutdownTimeout + time.Second):
		t.Fatal("ServeContext did not return promptly after the handler observed cancellation and returned")
	}
}

// --- stdio transport (ядро↔адаптер) ---

func TestServeStdioOneShot(t *testing.T) {
	t.Parallel()
	reqLine, _ := json.Marshal(Request{Cmd: "ping"})
	var out bytes.Buffer
	err := ServeStdio(bytes.NewReader(reqLine), &out, func(req Request) Response {
		if req.Cmd != "ping" {
			return Fail(errors.New("bad cmd"))
		}
		return OK(StatusData{Running: true, LastCheck: "x"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("resp.OK = false: %s", resp.Error)
	}
	var sd StatusData
	if err := json.Unmarshal(resp.Data, &sd); err != nil {
		t.Fatal(err)
	}
	if !sd.Running || sd.LastCheck != "x" {
		t.Fatalf("decoded = %+v", sd)
	}
}

func TestServeStdioBadRequestStillReplies(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	// Not valid JSON: ServeStdio must still emit an {ok:false} Response, not error out silently.
	if err := ServeStdio(strings.NewReader("{not json"), &out, func(Request) Response { return OK(nil) }); err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error == "" {
		t.Fatalf("want {ok:false} with an error, got %+v", resp)
	}
}

func TestServeStdioRejectsOversizedRequest(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	input := strings.NewReader(`{"cmd":"` + strings.Repeat("x", maxRequestJSONBytes) + `"}`)
	if err := ServeStdio(input, &out, func(Request) Response { return OK(nil) }); err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.Contains(resp.Error, "exceeds") {
		t.Fatalf("response = %+v, want bounded-input failure", resp)
	}
}

func TestServeStdioRejectsValidJSONWithOversizedTrailingData(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	called := false
	input := strings.NewReader(`{"cmd":"ping"}` + strings.Repeat("x", maxRequestJSONBytes))
	if err := ServeStdio(input, &out, func(Request) Response { called = true; return OK(nil) }); err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || called {
		t.Fatalf("response=%+v handler_called=%t", resp, called)
	}
}

func TestServeRejectsOversizedSocketRequest(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortSocketDir(t), "d.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var called atomic.Bool
	go Serve(ln, func(Request) Response { called.Store(true); return OK(nil) })
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(`{"cmd":"` + strings.Repeat("x", maxRequestJSONBytes) + `"}`)); err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error == "" || called.Load() {
		t.Fatalf("response = %+v handler_called=%t, want bounded-input failure", resp, called.Load())
	}
}

func TestServeRejectsValidJSONWithOversizedSocketTrailingData(t *testing.T) {
	t.Parallel()
	sock := filepath.Join(shortSocketDir(t), "d.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var called atomic.Bool
	go Serve(ln, func(Request) Response { called.Store(true); return OK(nil) })
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte(`{"cmd":"ping"}` + strings.Repeat("x", maxRequestJSONBytes)))
	_ = conn.Close()
	if called.Load() {
		t.Fatal("handler was called for a request with oversized trailing data")
	}
}

// engineHelperMarkerFlag, followed by a per-test uniqueID(t), makes the test
// binary act as a fake stdio engine: TestEngineHelperProcess reads one
// Request and writes one Response, then exits. CallStdio re-spawns
// os.Args[0] (this test binary) pointed at that helper — the stdlib os/exec
// "TestHelperProcess" pattern, giving a real subprocess round-trip.
//
// This used to be a shared environment variable (UNI_CHAT_STDIO_HELPER=1)
// set via t.Setenv, which is incompatible with t.Parallel() (t.Setenv panics
// if called after t.Parallel(), and a process-wide env var is exactly the
// "общее состояние стенда" 12-test-contract.md:257 forbids test isolation
// from relying on) — an argv marker carries the same signal without
// mutating shared process state, and doubles as the unique-per-test marker
// that rule also requires.
// Deliberately does NOT start with "-": the re-spawned process is this same
// test binary, so its own argv is parsed by the stdlib "flag" package (via
// go test's flags) before our code ever sees it, and any argument starting
// with "-" that flag does not recognize is rejected outright ("flag
// provided but not defined: ...") rather than silently ignored. A
// non-flag-shaped token is left alone by the parser and reaches os.Args
// unchanged.
const engineHelperMarkerFlag = "uc-engine-helper-marker="

// helperMarkerFromArgs extracts the value passed via engineHelperMarkerFlag,
// or "" if this process was not launched as the engine helper.
func helperMarkerFromArgs(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, engineHelperMarkerFlag) {
			return strings.TrimPrefix(a, engineHelperMarkerFlag)
		}
	}
	return ""
}

func TestEngineHelperProcess(t *testing.T) {
	if helperMarkerFromArgs(os.Args) == "" {
		return // ordinary run: not the helper
	}
	_ = ServeStdio(os.Stdin, os.Stdout, func(req Request) Response {
		switch req.Cmd {
		case "sleep":
			time.Sleep(2 * time.Second)
			return OK(nil)
		case "flood-stdout":
			// Writes far more than CallStdio's maxEngineStdoutBytes budget
			// directly to the real stdout — bypassing ServeStdio's own
			// single-JSON-value response entirely — and exits immediately,
			// so the parent must treat "truncated" as its own explicit
			// failure rather than attempting (and failing) a JSON decode of
			// a bounded, garbage-filled prefix.
			_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), maxEngineStdoutBytes+4096))
			os.Exit(0)
		case "flood-stderr":
			// Exceeds maxEngineStderrBytes and embeds control bytes (an ANSI
			// color escape, a newline, a bell) that a malicious or broken
			// engine could use to forge what looks like an extra log line
			// in whatever eventually logs CallStdio's returned error. Exits
			// non-zero so the parent's runBoundedCommand error path — not a
			// clean response — is what carries this stderr content back.
			payload := append(bytes.Repeat([]byte("y"), maxEngineStderrBytes+4096), []byte("\x1b[31mFAKE-LOG-LINE\ninjected\x07")...)
			_, _ = os.Stderr.Write(payload)
			os.Exit(1)
		case "ping":
			return OK(StatusData{Running: true, LastCheck: "helper"})
		}
		return Fail(errors.New("unexpected cmd"))
	})
	os.Exit(0)
}

// TestCallStdioBoundsOversizedStdout proves the maxEngineStdoutBytes budget
// (protocol.go) actually caps what CallStdio will buffer from a spawned
// engine's stdout, and that hitting the cap produces its own explicit,
// actionable error rather than a confusing JSON-decode failure over a
// silently truncated response.
func TestCallStdioBoundsOversizedStdout(t *testing.T) {
	t.Parallel()
	_, err := CallStdio(os.Args[0], []string{"-test.run=^TestEngineHelperProcess$", engineHelperMarkerFlag + uniqueID(t)}, Request{Cmd: "flood-stdout"}, 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("exceeds %d bytes", maxEngineStdoutBytes)) {
		t.Fatalf("err = %v, want an explicit oversized-response error naming the %d byte limit", err, maxEngineStdoutBytes)
	}
}

// TestCallStdioSanitizesOversizedStderr proves both halves of the stderr
// hardening: the maxEngineStderrBytes budget actually truncates (with an
// explicit marker naming the limit, not a silent cut), and every control
// character an untrusted engine wrote — including ones chosen specifically
// to look like a forged log line or a terminal escape sequence — is stripped
// from the error text CallStdio returns.
func TestCallStdioSanitizesOversizedStderr(t *testing.T) {
	t.Parallel()
	_, err := CallStdio(os.Args[0], []string{"-test.run=^TestEngineHelperProcess$", engineHelperMarkerFlag + uniqueID(t)}, Request{Cmd: "flood-stderr"}, 10*time.Second)
	if err == nil {
		t.Fatal("want an error from the engine's non-zero exit")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\x1b\n\r\x07\t") {
		t.Fatalf("err = %q, want every control character (ANSI escape, newline, CR, bell, tab) stripped from the untrusted engine's stderr", msg)
	}
	if !strings.Contains(msg, fmt.Sprintf("truncated at %d bytes", maxEngineStderrBytes)) {
		t.Fatalf("err = %q, want an explicit truncation marker naming the %d byte stderr limit", msg, maxEngineStderrBytes)
	}
}

func TestCallStdioTimeoutKillsSlowChild(t *testing.T) {
	t.Parallel()
	started := time.Now()
	_, err := CallStdio(os.Args[0], []string{"-test.run=^TestEngineHelperProcess$", engineHelperMarkerFlag + uniqueID(t)}, Request{Cmd: "sleep"}, 20*time.Millisecond)
	if err == nil {
		t.Fatal("slow engine must be stopped when its bounded context expires")
	}
	if !strings.Contains(err.Error(), "engine timeout after 20ms") || !strings.Contains(err.Error(), "SIGTERM") {
		t.Fatalf("timeout error = %v, want actionable signal diagnostic", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timed out engine cleanup took %v, want bounded cleanup", elapsed)
	}
}

func TestServeConnectionTimeoutForKeepsSummaryBounded(t *testing.T) {
	t.Parallel()
	if got := serveConnectionTimeoutFor("sync"); got != 11*time.Minute {
		t.Fatalf("sync server timeout = %v, want 11m", got)
	}
	if got := serveConnectionTimeoutFor("delete_job_start"); got != deleteJobStartServeTimeout {
		t.Fatalf("job start server timeout = %v, want %v", got, deleteJobStartServeTimeout)
	}
	if got := serveConnectionTimeoutFor("delete_range_summary"); got != deleteRangeSummaryServeTimeout {
		t.Fatalf("summary server timeout = %v, want %v", got, deleteRangeSummaryServeTimeout)
	}
	if deleteRangeSummaryServeTimeout != 11*time.Minute {
		t.Fatalf("summary server timeout constant = %v, want 11m", deleteRangeSummaryServeTimeout)
	}
	if got := serveConnectionTimeoutFor("ping"); got != 120*time.Second {
		t.Fatalf("ordinary server timeout = %v, want 120s", got)
	}
	if got := serveConnectionTimeoutFor("delete_range_chunk"); got != serveConnectionTimeout {
		t.Fatalf("chunk server timeout = %v, want %v", got, serveConnectionTimeout)
	}
	if got := serveConnectionTimeoutFor("delete_batch"); got != serveConnectionTimeout {
		t.Fatalf("batch server timeout = %v, want %v", got, serveConnectionTimeout)
	}
	if SyncServeTimeout != 11*time.Minute {
		t.Fatalf("socket sync timeout = %v, want 11m", SyncServeTimeout)
	}
}

func TestServeConnectionTimeoutForStartDoesNotUseDefaultDeadline(t *testing.T) {
	t.Parallel()
	if deleteJobStartServeTimeout != DeleteJobStartTimeout {
		t.Fatalf("job start timeout mismatch: socket=%v protocol=%v", deleteJobStartServeTimeout, DeleteJobStartTimeout)
	}
	if serveConnectionTimeoutFor("delete_job_start") <= deleteRangeSummaryServeTimeout {
		t.Fatal("job start socket timeout must cover the summary deadline")
	}
	if serveConnectionTimeoutFor("delete_job_status") != serveConnectionTimeout {
		t.Fatal("job status must retain the normal socket timeout")
	}
	if serveConnectionTimeoutFor("delete_job_cancel") != serveConnectionTimeout {
		t.Fatal("job cancel must retain the normal socket timeout")
	}
}

func TestCallStdioRoundTrip(t *testing.T) {
	t.Parallel()
	resp, err := CallStdio(os.Args[0], []string{"-test.run=^TestEngineHelperProcess$", engineHelperMarkerFlag + uniqueID(t)}, Request{Cmd: "ping"}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("resp.OK = false: %s", resp.Error)
	}
	var sd StatusData
	if err := json.Unmarshal(resp.Data, &sd); err != nil {
		t.Fatal(err)
	}
	if !sd.Running || sd.LastCheck != "helper" {
		t.Fatalf("decoded = %+v", sd)
	}
}

func TestCallStdioSpawnFailure(t *testing.T) {
	t.Parallel()
	_, err := CallStdio("/nonexistent/engine/binary", []string{"engine-serve"}, Request{Cmd: "ping"}, 5*time.Second)
	if err == nil {
		t.Fatal("want an error when the engine binary cannot be spawned")
	}
}

func TestEnginePaths(t *testing.T) {
	t.Setenv("UNI_CHAT_DIR", "/tmp/uc-test")
	if got := EnginesPath(); got != "/tmp/uc-test/engines.json" {
		t.Fatalf("EnginesPath = %q", got)
	}
	if got := EngineDir("mattermost"); got != "/tmp/uc-test/engines/mattermost" {
		t.Fatalf("EngineDir = %q", got)
	}
}

// --- TUI verbs: channels / history / send ---

func TestCheckItemCarriesCreatedAt(t *testing.T) {
	in := CheckItem{
		ChannelID:  "c1",
		ChannelRef: "unisender/dev",
		Sender:     "bob",
		Message:    "hi",
		PostID:     "p1",
		Kind:       "mention",
		CreatedAt:  "2023-11-14T22:13:20Z",
		DeepLink:   "mattermost://mm/unisender/pl/p1",
		WebLink:    "https://mm/unisender/pl/p1",
		Reactions:  []string{"white_check_mark"},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"created_at":"2023-11-14T22:13:20Z"`) {
		t.Fatalf("encoded = %s, want a created_at field", b)
	}
	if !strings.Contains(string(b), `"reactions":["white_check_mark"]`) {
		t.Fatalf("encoded = %s, want a reactions field", b)
	}
	var out CheckItem
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	// CheckItem carries a slice field (Reactions) since this test was written,
	// so it can no longer be compared with != — reflect.DeepEqual instead.
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
}

// TestCheckItemCreatedAtOmittedWhenEmpty proves the field is additive: an
// engine that never fills it produces exactly the old wire shape, so no
// current consumer of CheckItem changes behaviour.
func TestCheckItemCreatedAtOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(CheckItem{PostID: "p1", Kind: "dm"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "created_at") {
		t.Fatalf("encoded = %s, want no created_at key when the field is empty", b)
	}
	if strings.Contains(string(b), "reactions") {
		t.Fatalf("encoded = %s, want no reactions key when the field is empty", b)
	}
}

func TestCheckItemAddressFlagsAreOptionalAndRoundTrip(t *testing.T) {
	in := CheckItem{PostID: "p1", Own: true, Addressed: true}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"own":true`) || !strings.Contains(string(b), `"addressed":true`) {
		t.Fatalf("encoded = %s, want both address flags", b)
	}
	var out CheckItem
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}

	b, err = json.Marshal(CheckItem{PostID: "p2"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "own") || strings.Contains(string(b), "addressed") {
		t.Fatalf("encoded = %s, want false address flags omitted", b)
	}
}

// TestCheckItemThreadRootIDIsOptionalAndRoundTrips proves the thread id is
// additive: a filled value survives the round trip, an empty one leaves the
// wire shape byte-identical to what an engine without threads already sends.
func TestCheckItemThreadRootIDIsOptionalAndRoundTrips(t *testing.T) {
	in := CheckItem{PostID: "p2", ThreadRootID: "p1"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"thread_root_id":"p1"`) {
		t.Fatalf("encoded = %s, want a thread_root_id field", b)
	}
	var out CheckItem
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}

	b, err = json.Marshal(CheckItem{PostID: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "thread_root_id") {
		t.Fatalf("encoded = %s, want no thread_root_id key when the post is not in a thread", b)
	}
}

func TestDeleteBatchJSONRoundTrip(t *testing.T) {
	in := DeleteBatchArgs{ChannelID: "mattermost/c1", PostIDs: []string{"p1", "p2"}, Snapshot: "stable", RangeChunk: true}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out DeleteBatchArgs
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
}

func TestDeletePreviewJSONRoundTripKeepsChannelRefSeparateFromChannelID(t *testing.T) {
	in := DeletePreviewArgs{Engine: "mattermost", Channel: "unisender/releases", From: time.UnixMilli(1000).UTC(), To: time.UnixMilli(2000).UTC(), IncludeThreadRoots: true, WantChannelKey: true}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out DeletePreviewArgs
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) || out.ChannelID != "" {
		t.Fatalf("round trip = %+v, want channel ref without opaque channel id", out)
	}
	if !strings.Contains(string(b), `"want_channel_key":true`) {
		t.Fatalf("encoded = %s, want want_channel_key=true", b)
	}
}

func TestDeletePreviewDataJSONRoundTripKeepsRangeSafetyMetadata(t *testing.T) {
	in := DeletePreviewData{ChannelID: "c1", ChannelKey: "mattermost/team/channel", Requested: 4, Targets: []DeleteTarget{{PostID: "reply"}}, SkippedRootIDs: []string{"root"}, Snapshot: "stable"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out DeletePreviewData
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
}

func TestDeleteChannelKeyWireCompatibility(t *testing.T) {
	rangeArgs := DeleteRangeSummaryArgs{Channel: "mattermost/team/channel", From: time.UnixMilli(1000).UTC(), To: time.UnixMilli(2000).UTC(), WantChannelKey: true}
	raw, err := json.Marshal(rangeArgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"want_channel_key":true`) {
		t.Fatalf("encoded range args = %s, want want_channel_key=true", raw)
	}
	var decodedRangeArgs DeleteRangeSummaryArgs
	if err := json.Unmarshal(raw, &decodedRangeArgs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodedRangeArgs, rangeArgs) {
		t.Fatalf("range args round trip = %+v, want %+v", decodedRangeArgs, rangeArgs)
	}

	rangeData := DeleteRangeSummaryData{ChannelID: "c1", ChannelKey: "mattermost/team/channel", TeamID: "t1"}
	raw, err = json.Marshal(rangeData)
	if err != nil {
		t.Fatal(err)
	}
	var decodedRangeData DeleteRangeSummaryData
	if err := json.Unmarshal(raw, &decodedRangeData); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodedRangeData, rangeData) {
		t.Fatalf("range data round trip = %+v, want %+v", decodedRangeData, rangeData)
	}

	var oldPreviewArgs DeletePreviewArgs
	if err := json.Unmarshal([]byte(`{"engine":"mattermost","channel":"team/channel","post_ids":["p1"]}`), &oldPreviewArgs); err != nil {
		t.Fatal(err)
	}
	if oldPreviewArgs.WantChannelKey {
		t.Fatal("legacy preview args unexpectedly requested channel key")
	}
	var oldRangeArgs DeleteRangeSummaryArgs
	if err := json.Unmarshal([]byte(`{"channel":"team/channel","from":"1970-01-01T00:00:01Z","to":"1970-01-01T00:00:02Z"}`), &oldRangeArgs); err != nil {
		t.Fatal(err)
	}
	if oldRangeArgs.WantChannelKey {
		t.Fatal("legacy range args unexpectedly requested channel key")
	}
	var oldPreviewData DeletePreviewData
	if err := json.Unmarshal([]byte(`{"channel_id":"c1","requested":1,"targets":[],"requires_elevated_auth":false,"snapshot":"stable"}`), &oldPreviewData); err != nil {
		t.Fatal(err)
	}
	if oldPreviewData.ChannelKey != "" {
		t.Fatalf("legacy preview data channel key = %q, want empty", oldPreviewData.ChannelKey)
	}
	var oldRangeData DeleteRangeSummaryData
	if err := json.Unmarshal([]byte(`{"channel_id":"c1","team_id":"t1","requested":1,"effective":1,"skipped_roots":0,"protected_roots":0,"include_thread_roots":false,"requires_elevated_auth":false}`), &oldRangeData); err != nil {
		t.Fatal(err)
	}
	if oldRangeData.ChannelKey != "" {
		t.Fatalf("legacy range data channel key = %q, want empty", oldRangeData.ChannelKey)
	}
}

func TestChannelsDataRoundTrip(t *testing.T) {
	in := ChannelsData{Channels: []ChannelInfo{
		{Engine: "mattermost", ChannelID: "mattermost/c7", ChannelRef: "mattermost/unisender/ops", Kind: "watch", Always: true, LastActivity: "2026-07-31T10:00:00Z"},
		{Engine: "mattermost", ChannelID: "mattermost/c9", ChannelRef: "mattermost/[dm]", DisplayName: "Bob", Kind: "dm"},
	}, Mode: "recent"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ChannelsData
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Channels) != 2 || out.Channels[0] != in.Channels[0] || out.Channels[1] != in.Channels[1] {
		t.Fatalf("round trip channels = %+v, want %+v", out.Channels, in.Channels)
	}
	if out.Mode != in.Mode {
		t.Fatalf("round trip mode = %q, want %q", out.Mode, in.Mode)
	}
}

func TestCapabilitiesRoundTripAndLegacyResponseCompatibility(t *testing.T) {
	in := CapabilitiesData{Engines: []EngineCapabilitiesData{{Engine: "mattermost", Capabilities: []CapabilityInfo{{ID: "messages.send", Label: "Send messages", Action: "messages.send", Status: "restricted", Reason: &CapabilityReason{Code: "api_key_restricted"}}, {ID: "channels.list", Status: "available"}}}}, Partial: true, Errors: []SyncError{{Engine: "telegram", Error: "unavailable"}}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out CapabilitiesData
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("capabilities round trip = %+v, want %+v", out, in)
	}
	var old Response
	if err := json.Unmarshal([]byte(`{"ok":true,"data":{"running":true}}`), &old); err != nil {
		t.Fatal(err)
	}
	if !old.OK || string(old.Data) != `{"running":true}` {
		t.Fatalf("old response compatibility = %+v", old)
	}
}

// TestChannelsDataOmitsTheModeKeyWhenUnset is what keeps an adapter's reply
// byte-for-byte what it is today: ChannelsData is also the type engines answer
// in, they have never filled the mode in (the router does, on the merged
// reply), and omitempty means their JSON gains no key at all.
func TestChannelsDataOmitsTheModeKeyWhenUnset(t *testing.T) {
	b, err := json.Marshal(ChannelsData{Channels: []ChannelInfo{
		{Engine: "mattermost", ChannelID: "c1", ChannelRef: "dev", Kind: "watch"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"mode"`) {
		t.Fatalf("encoded = %s, want no mode key when the engine did not set one", b)
	}
}

func TestChannelsArgsRoundTrip(t *testing.T) {
	in := ChannelsArgs{Since: "2026-07-31T10:00:00Z", Mode: "recent"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ChannelsArgs
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
}

func TestChannelInfoPendingRoundTrip(t *testing.T) {
	in := ChannelsData{Channels: []ChannelInfo{{
		ChannelID:  "mattermost/c1",
		ChannelRef: "mattermost/unisender/dev",
		Kind:       "mention",
		Pending:    &CheckItem{ChannelID: "mattermost/c1", ChannelRef: "mattermost/unisender/dev", PostID: "p9", Sender: "bob", Message: "ping", CreatedAt: "2026-07-31T10:00:00Z", Addressed: true},
	}}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ChannelsData
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Channels) != 1 || out.Channels[0].Pending == nil || out.Channels[0].Pending.PostID != "p9" || !out.Channels[0].Pending.Addressed {
		t.Fatalf("round trip = %+v, want pending p9 with addressed=true", out.Channels)
	}
}

// TestHistoryArgsOmitsOptionalFields proves "the most recent page" is encoded
// as the absence of before/limit, not as zero values the adapter would have to
// special-case on the wire.
func TestHistoryArgsOmitsOptionalFields(t *testing.T) {
	b, err := json.Marshal(HistoryArgs{ChannelID: "mattermost/c1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "before") || strings.Contains(string(b), "limit") {
		t.Fatalf("encoded = %s, want neither before nor limit for the newest page", b)
	}
	b, err = json.Marshal(HistoryArgs{ChannelID: "mattermost/c1", Before: "p9", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"before":"p9"`) || !strings.Contains(string(b), `"limit":50`) {
		t.Fatalf("encoded = %s, want before and limit carried", b)
	}
}

// TestHistoryArgsChannelIsTheAlternativeToChannelID proves the CLI's own path
// into history (a human ref, resolved server-side) round-trips independently
// of the TUI's path (an already-known id) — and that each is absent from the
// wire when unused, so an old adapter build ignoring the new field sees
// exactly the ChannelID-only shape it always has.
func TestHistoryArgsChannelIsTheAlternativeToChannelID(t *testing.T) {
	b, err := json.Marshal(HistoryArgs{Channel: "mattermost/devteam/release", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"channel":"mattermost/devteam/release"`) {
		t.Fatalf("encoded = %s, want the channel field carried", b)
	}
	if strings.Contains(string(b), "channel_id") {
		t.Fatalf("encoded = %s, want no channel_id key when only Channel is set", b)
	}

	var out HistoryArgs
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Channel != "mattermost/devteam/release" || out.ChannelID != "" {
		t.Fatalf("decoded = %+v, want Channel set and ChannelID empty", out)
	}
}

// TestHistoryArgsThreadRootIDIsAdditiveAndRoundTrips pins the whole contract of
// the thread-context request in one place: the field is absent from the wire
// unless it is asked for (so an adapter built before it sees byte-for-byte the
// shape it always saw), it survives HistoryArgs' own hand-written
// Marshal/Unmarshal pair — which is exactly where a new field silently goes
// missing, since neither is generated from the struct tags — and it travels
// alongside ChannelID rather than replacing it, because the thread still has to
// be looked up inside a channel.
func TestHistoryArgsThreadRootIDIsAdditiveAndRoundTrips(t *testing.T) {
	b, err := json.Marshal(HistoryArgs{ChannelID: "mattermost/c1", Before: "p9"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "thread_root_id") {
		t.Fatalf("encoded = %s, want no thread_root_id key on an ordinary page request", b)
	}

	b, err = json.Marshal(HistoryArgs{ChannelID: "mattermost/c1", ThreadRootID: "root9"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"thread_root_id":"root9"`) || !strings.Contains(string(b), `"channel_id":"mattermost/c1"`) {
		t.Fatalf("encoded = %s, want thread_root_id carried next to channel_id", b)
	}

	var out HistoryArgs
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.ThreadRootID != "root9" || out.ChannelID != "mattermost/c1" {
		t.Fatalf("decoded = %+v, want ThreadRootID root9 in channel mattermost/c1", out)
	}
}

func TestHistoryDataRoundTrip(t *testing.T) {
	in := HistoryData{
		Items:        []CheckItem{{PostID: "p1", Kind: "watch", CreatedAt: "2023-11-14T22:13:20Z"}, {PostID: "p2", Kind: "watch"}},
		HasMore:      true,
		BeforeCursor: "server-cursor",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out HistoryData
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 2 || out.Items[0].PostID != "p1" || out.Items[1].PostID != "p2" || !out.HasMore || out.BeforeCursor != "server-cursor" {
		t.Fatalf("round trip = %+v (has_more=%v)", out.Items, out.HasMore)
	}
}

func TestHistoryArgsUsesTimeWireNamesAndPreservesExplicitZero(t *testing.T) {
	a := HistoryArgs{SinceTime: time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC), MaxPagesSet: true}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"since_time":"2026-08-04T10:00:00Z"`) || !strings.Contains(string(b), `"max_pages":0`) || strings.Contains(string(b), `"since":`) {
		t.Fatalf("wire = %s", b)
	}
	var out HistoryArgs
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.SinceTime.IsZero() || !out.MaxPagesSet || out.MaxPages != 0 {
		t.Fatalf("decoded = %+v", out)
	}
}

func TestCheckArgsRefreshAndSyncDataWireContract(t *testing.T) {
	b := MarshalArgs(CheckArgs{Refresh: true})
	if string(b) != `{"refresh":true}` {
		t.Fatalf("refresh args = %s", b)
	}
	var data SyncData
	if err := json.Unmarshal([]byte(`{"items":[],"partial":true,"truncated":true,"errors":[{"engine":"mattermost","channel_id":"c1","error":"boom"}]}`), &data); err != nil {
		t.Fatal(err)
	}
	if !data.Partial || !data.Truncated || len(data.Errors) != 1 || data.Errors[0].Engine != "mattermost" {
		t.Fatalf("sync data = %+v", data)
	}
}

func TestCheckCursorsArgsWireContract(t *testing.T) {
	b := MarshalArgs(CheckCursorsArgs{Cursors: map[string]string{"channel-1": "native-9"}})
	if string(b) != `{"cursors":{"channel-1":"native-9"}}` {
		t.Fatalf("cursor check args = %s", b)
	}
	var args CheckCursorsArgs
	if err := json.Unmarshal(b, &args); err != nil {
		t.Fatal(err)
	}
	if args.Cursors["channel-1"] != "native-9" || args.IncludeOwn {
		t.Fatalf("decoded = %+v", args)
	}
}

func TestSendRoundTrip(t *testing.T) {
	args := SendArgs{ChannelID: "mattermost/c1", Text: "deploying now"}
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	var gotArgs SendArgs
	if err := json.Unmarshal(b, &gotArgs); err != nil {
		t.Fatal(err)
	}
	if gotArgs != args {
		t.Fatalf("args round trip = %+v, want %+v", gotArgs, args)
	}
	data := SendData{Item: CheckItem{PostID: "pNEW", Sender: "sergey", Message: "deploying now", CreatedAt: "2023-11-14T22:13:20Z"}}
	b, err = json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var gotData SendData
	if err := json.Unmarshal(b, &gotData); err != nil {
		t.Fatal(err)
	}
	// CheckItem carries a slice field (Reactions), so it can no longer be
	// compared with != — reflect.DeepEqual instead.
	if !reflect.DeepEqual(gotData.Item, data.Item) {
		t.Fatalf("data round trip = %+v, want %+v", gotData.Item, data.Item)
	}
}

func TestReactRoundTrip(t *testing.T) {
	args := ReactArgs{ChannelID: "mattermost/c1", PostID: "p1", Emoji: "white_check_mark"}
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	var gotArgs ReactArgs
	if err := json.Unmarshal(b, &gotArgs); err != nil {
		t.Fatal(err)
	}
	if gotArgs != args {
		t.Fatalf("args round trip = %+v, want %+v", gotArgs, args)
	}
	data := ReactData{UserID: "u1", PostID: "p1", Emoji: "white_check_mark"}
	b, err = json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var gotData ReactData
	if err := json.Unmarshal(b, &gotData); err != nil {
		t.Fatal(err)
	}
	if gotData != data {
		t.Fatalf("data round trip = %+v, want %+v", gotData, data)
	}
}

func TestEditRoundTrip(t *testing.T) {
	args := EditArgs{ChannelID: "mattermost/c1", PostID: "p1", Text: "corrected text"}
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	var gotArgs EditArgs
	if err := json.Unmarshal(b, &gotArgs); err != nil {
		t.Fatal(err)
	}
	if gotArgs != args {
		t.Fatalf("args round trip = %+v, want %+v", gotArgs, args)
	}
	data := EditData{Permalink: "https://mattermost.example/team/pl/p1", PostID: "p1"}
	b, err = json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var gotData EditData
	if err := json.Unmarshal(b, &gotData); err != nil {
		t.Fatal(err)
	}
	if gotData != data {
		t.Fatalf("data round trip = %+v, want %+v", gotData, data)
	}
}

func TestGetPostRoundTrip(t *testing.T) {
	args := GetPostArgs{ChannelID: "mattermost/c1", PostID: "p1"}
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	var gotArgs GetPostArgs
	if err := json.Unmarshal(b, &gotArgs); err != nil {
		t.Fatal(err)
	}
	if gotArgs != args {
		t.Fatalf("args round trip = %+v, want %+v", gotArgs, args)
	}
	data := GetPostData{Item: CheckItem{ChannelID: "c1", PostID: "p1", Sender: "alice", Message: "hi", ThreadRootID: "p0", CreatedAt: "2026-01-02T03:04:05Z"}}
	b, err = json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var gotData GetPostData
	if err := json.Unmarshal(b, &gotData); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotData, data) {
		t.Fatalf("data round trip = %+v, want %+v", gotData, data)
	}
}

func TestGetPostArgsValidateRejectsEmptyPostID(t *testing.T) {
	if err := (GetPostArgs{Engine: "mattermost"}).Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for empty post_id")
	}
	if err := (GetPostArgs{Engine: "mattermost", PostID: "p1"}).Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// TestSendArgsRootPostIDIsOptionalAndRoundTrips proves a reply into a thread is
// an additive request shape: without it the wire form is exactly the old
// {channel_id, text}.
func TestSendArgsRootPostIDIsOptionalAndRoundTrips(t *testing.T) {
	in := SendArgs{ChannelID: "mattermost/c9", Text: "on it", RootPostID: "p2"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"root_post_id":"p2"`) {
		t.Fatalf("encoded = %s, want a root_post_id field", b)
	}
	var out SendArgs
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}

	b, err = json.Marshal(SendArgs{ChannelID: "mattermost/c9", Text: "on it"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "root_post_id") {
		t.Fatalf("encoded = %s, want no root_post_id key for a top-level reply", b)
	}
}

func TestPostDataRoundTripsOptionalIdentifiers(t *testing.T) {
	want := PostData{Permalink: "https://mm.example/pl/p1", ChannelID: "c1", PostID: "p1"}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got PostData
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("post data = %+v, want %+v", got, want)
	}
	legacy := []byte(`{"permalink":"https://mm.example/pl/legacy"}`)
	var old PostData
	if err := json.Unmarshal(legacy, &old); err != nil {
		t.Fatal(err)
	}
	if old.Permalink == "" || old.ChannelID != "" || old.PostID != "" {
		t.Fatalf("legacy post data = %+v", old)
	}
}

// TestPostArgsRootPostIDIsOptionalAndRoundTrips proves a reply into a thread
// is an additive request shape for "post" too: without it the wire form is
// exactly the old {channel, text}.
func TestPostArgsRootPostIDIsOptionalAndRoundTrips(t *testing.T) {
	in := PostArgs{Channel: "mattermost/c9", Text: "on it", RootPostID: "p2"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"root_post_id":"p2"`) {
		t.Fatalf("encoded = %s, want a root_post_id field", b)
	}
	var out PostArgs
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}

	b, err = json.Marshal(PostArgs{Channel: "mattermost/c9", Text: "on it"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "root_post_id") {
		t.Fatalf("encoded = %s, want no root_post_id key for a top-level post", b)
	}
}

func TestCheckItemCarriesReactionDetails(t *testing.T) {
	in := CheckItem{
		ChannelID: "c1",
		PostID:    "p1",
		Reactions: []string{"white_check_mark"},
		ReactionDetails: []ReactionDetail{
			{Emoji: "white_check_mark", Users: []string{"alice", "bob"}},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"reaction_details":[{"emoji":"white_check_mark","users":["alice","bob"]}]`) {
		t.Fatalf("encoded = %s, want a reaction_details field", b)
	}
	var out CheckItem
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
}

func TestCheckItemReactionDetailsOmittedWhenEmpty(t *testing.T) {
	in := CheckItem{ChannelID: "c1", PostID: "p1"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "reaction_details") {
		t.Fatalf("encoded = %s, want no reaction_details key when empty", b)
	}
}

func TestSearchItemCarriesReactionDetails(t *testing.T) {
	in := SearchItem{
		PostID:    "p1",
		Reactions: []string{"eyes"},
		ReactionDetails: []ReactionDetail{
			{Emoji: "eyes", Users: []string{"carol"}},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"reaction_details":[{"emoji":"eyes","users":["carol"]}]`) {
		t.Fatalf("encoded = %s, want a reaction_details field", b)
	}
	var out SearchItem
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
}
