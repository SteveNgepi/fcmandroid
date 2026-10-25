package mcs

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/SteveNgepi/fcmandroid/internal/pb"

	"google.golang.org/protobuf/proto"
)

// fakeServer drives the server side of an MCS connection over net.Pipe. It
// reads the client version byte plus the first stanza (the LoginRequest), then
// writes its own version byte and whatever stanzas the test queues.
type fakeServer struct {
	t    *testing.T
	rw   net.Conn
	r    *bufio.Reader
	sent bool
}

func newFakeServer(t *testing.T, c net.Conn) *fakeServer {
	return &fakeServer{t: t, rw: c, r: bufio.NewReader(c)}
}

func (s *fakeServer) readVersion() byte {
	s.t.Helper()
	v, err := s.r.ReadByte()
	if err != nil {
		s.t.Fatalf("server read version: %v", err)
	}
	return v
}

func (s *fakeServer) readStanza() (byte, []byte) {
	s.t.Helper()
	tag, err := s.r.ReadByte()
	if err != nil {
		s.t.Fatalf("server read tag: %v", err)
	}
	n, err := binary.ReadUvarint(s.r)
	if err != nil {
		s.t.Fatalf("server read len: %v", err)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(s.r, buf); err != nil {
		s.t.Fatalf("server read payload: %v", err)
	}
	return tag, buf
}

func (s *fakeServer) writeStanza(tag byte, msg proto.Message) {
	s.t.Helper()
	b, err := proto.MarshalOptions{AllowPartial: true}.Marshal(msg)
	if err != nil {
		s.t.Fatalf("server marshal: %v", err)
	}
	var hdr []byte
	if !s.sent {
		hdr = append(hdr, Version)
		s.sent = true
	}
	hdr = append(hdr, tag)
	var lv [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lv[:], uint64(len(b)))
	hdr = append(hdr, lv[:n]...)
	hdr = append(hdr, b...)
	// One write so a reset that closes the pipe mid-frame cannot tear a header
	// from its payload. A closed pipe during teardown is expected, not fatal.
	if _, err := s.rw.Write(hdr); err != nil {
		s.t.Logf("server write: %v", err)
	}
}

func okLoginResponse() *pb.LoginResponse {
	return &pb.LoginResponse{Id: new("1")}
}

func errLoginResponse() *pb.LoginResponse {
	return &pb.LoginResponse{
		Id:    new("1"),
		Error: &pb.ErrorInfo{Code: proto.Int32(401), Message: new("bad")},
	}
}

// login performs the server side of the handshake: read the client version
// byte and LoginRequest, then reply with version byte and an ok response.
func (s *fakeServer) login() {
	s.t.Helper()
	s.readVersion()
	if tag, _ := s.readStanza(); tag != TagLoginRequest {
		s.t.Fatalf("first stanza tag = %d, want LoginRequest", tag)
	}
	s.writeStanza(TagLoginResponse, okLoginResponse())
}

// clientLogin drives the client side of the handshake so the version byte is
// consumed before Run takes over the reader.
func clientLogin(t *testing.T, c *Conn) {
	t.Helper()
	lr := BuildLoginRequest(LoginConfig{AndroidID: 1, SecurityToken: 2, ClientID: "x"})
	if err := c.Login(lr); err != nil {
		t.Fatalf("Login: %v", err)
	}
}

func sampleData(pid, key, val string) *pb.DataMessageStanza {
	return &pb.DataMessageStanza{
		From:         new("123"),
		Category:     new("receiver.push.com"),
		PersistentId: new(pid),
		AppData:      []*pb.AppData{{Key: new(key), Value: new(val)}},
		RawData:      []byte("ciphertext"),
	}
}

func deletedMessages(pid string) *pb.DataMessageStanza {
	return &pb.DataMessageStanza{
		From:         new("123"),
		Category:     new("receiver.push.com"),
		PersistentId: new(pid),
		AppData:      []*pb.AppData{{Key: new("message_type"), Value: new("deleted_messages")}},
	}
}

func TestBuildLoginRequest(t *testing.T) {
	lr := BuildLoginRequest(LoginConfig{
		AndroidID:     0x1a2b3c,
		SecurityToken: 9876543210,
		ClientID:      "94.0.4606.51",
		HeartbeatMS:   10000,
	})

	if got := lr.GetAuthToken(); got != "9876543210" {
		t.Errorf("auth_token = %q, want decimal security token 9876543210", got)
	}
	if got := lr.GetDeviceId(); got != "android-1a2b3c" {
		t.Errorf("device_id = %q, want android-1a2b3c", got)
	}
	if got := lr.GetUser(); got != "1715004" {
		t.Errorf("user = %q, want decimal android id 1715004", got)
	}
	if lr.GetAuthService() != pb.LoginRequest_ANDROID_ID {
		t.Errorf("auth_service = %v, want ANDROID_ID", lr.GetAuthService())
	}

	var foundVC bool
	for _, s := range lr.GetSetting() {
		if s.GetName() == "new_vc" {
			foundVC = true
			if s.GetValue() != "1" {
				t.Errorf("new_vc value = %q, want 1", s.GetValue())
			}
		}
	}
	if !foundVC {
		t.Error("new_vc setting missing")
	}

	if lr.GetHeartbeatStat() == nil {
		t.Fatal("heartbeat_stat missing when HeartbeatMS > 0")
	}
	if lr.GetHeartbeatStat().GetIntervalMs() != 10000 {
		t.Errorf("interval_ms = %d, want 10000", lr.GetHeartbeatStat().GetIntervalMs())
	}
}

func TestBuildLoginRequestNoHeartbeat(t *testing.T) {
	lr := BuildLoginRequest(LoginConfig{AndroidID: 1, SecurityToken: 2, ClientID: "x"})
	if lr.GetHeartbeatStat() != nil {
		t.Error("heartbeat_stat should be nil when HeartbeatMS <= 0")
	}
}

func TestLoginOK(t *testing.T) {
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	s := newFakeServer(t, srv)

	go func() {
		if v := s.readVersion(); v != Version {
			t.Errorf("client version byte = %d, want %d", v, Version)
		}
		if tag, _ := s.readStanza(); tag != TagLoginRequest {
			t.Errorf("first stanza tag = %d, want LoginRequest", tag)
		}
		s.writeStanza(TagLoginResponse, okLoginResponse())
	}()

	c := NewConn(cli)
	lr := BuildLoginRequest(LoginConfig{AndroidID: 1, SecurityToken: 2, ClientID: "x"})
	if err := c.Login(lr); err != nil {
		t.Fatalf("Login returned error: %v", err)
	}
}

func TestLoginError(t *testing.T) {
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	s := newFakeServer(t, srv)

	go func() {
		s.readVersion()
		s.readStanza()
		s.writeStanza(TagLoginResponse, errLoginResponse())
	}()

	c := NewConn(cli)
	lr := BuildLoginRequest(LoginConfig{AndroidID: 1, SecurityToken: 2, ClientID: "x"})
	if err := c.Login(lr); err == nil {
		t.Fatal("Login should return an error on a non-nil LoginResponse error")
	}
}

func TestRunDataSelectiveAck(t *testing.T) {
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	s := newFakeServer(t, srv)

	go func() {
		s.login()
		s.writeStanza(TagDataMessageStanza, sampleData("pid-1", "k", "v"))
	}()

	gotPID := make(chan string, 1)
	gotData := make(chan map[string]string, 1)
	c := NewConn(cli)
	clientLogin(t, c)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- c.Run(ctx, Handlers{
			OnData: func(d Data) error {
				gotPID <- d.PersistentID
				gotData <- d.AppData
				return nil
			},
		}, RunParams{SelectiveAck: true})
	}()

	select {
	case pid := <-gotPID:
		if pid != "pid-1" {
			t.Errorf("OnData pid = %q, want pid-1", pid)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnData was not called")
	}
	if ad := <-gotData; ad["k"] != "v" {
		t.Errorf("appData = %v, want k=v", ad)
	}

	// The client should write back a selective-ack IqStanza.
	tag, payload := s.readStanza()
	if tag != TagIqStanza {
		t.Fatalf("client wrote tag %d, want IqStanza", tag)
	}
	var iq pb.IqStanza
	if err := proto.Unmarshal(payload, &iq); err != nil {
		t.Fatalf("unmarshal iq: %v", err)
	}
	if iq.GetExtension().GetId() != SelectiveAckExtensionID {
		t.Errorf("extension id = %d, want %d", iq.GetExtension().GetId(), SelectiveAckExtensionID)
	}
	var sa pb.SelectiveAck
	if err := proto.Unmarshal(iq.GetExtension().GetData(), &sa); err != nil {
		t.Fatalf("unmarshal selective ack: %v", err)
	}
	if len(sa.GetId()) != 1 || sa.GetId()[0] != "pid-1" {
		t.Errorf("selective ack ids = %v, want [pid-1]", sa.GetId())
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Errorf("Run returned %v, want nil on cancel", err)
	}
}

func TestRunStreamIDAck(t *testing.T) {
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	s := newFakeServer(t, srv)

	go func() {
		s.login()
		s.writeStanza(TagDataMessageStanza, sampleData("pid-1", "k", "v"))
		s.writeStanza(TagHeartbeatPing, &pb.HeartbeatPing{})
	}()

	c := NewConn(cli)
	clientLogin(t, c)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- c.Run(ctx, Handlers{
			OnData: func(Data) error { return nil },
		}, RunParams{SelectiveAck: true})
	}()

	// Outbound order: selective-ack for the data message, then the heartbeat
	// ack for the ping. Find the HeartbeatAck and check its cumulative id.
	var ackStreamID int32 = -1
	deadline := time.After(2 * time.Second)
	for ackStreamID < 0 {
		select {
		case <-deadline:
			t.Fatal("did not see a HeartbeatAck")
		default:
		}
		tag, payload := s.readStanza()
		switch tag {
		case TagIqStanza:
			// selective ack, skip
		case TagHeartbeatAck:
			var ack pb.HeartbeatAck
			if err := proto.Unmarshal(payload, &ack); err != nil {
				t.Fatalf("unmarshal ack: %v", err)
			}
			ackStreamID = ack.GetLastStreamIdReceived()
		default:
			t.Fatalf("unexpected outbound tag %d", tag)
		}
	}
	// LoginResponse is stream id 1, the data message is 2, the ping is 3, so the
	// cumulative ack the ping triggers reports 3.
	if ackStreamID != 3 {
		t.Errorf("HeartbeatAck last_stream_id_received = %d, want 3", ackStreamID)
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Errorf("Run returned %v, want nil on cancel", err)
	}
}

func TestRunCloseResets(t *testing.T) {
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	s := newFakeServer(t, srv)

	go func() {
		s.login()
		s.writeStanza(TagClose, &pb.Close{})
	}()

	c := NewConn(cli)
	clientLogin(t, c)
	err := c.Run(context.Background(), Handlers{
		OnData: func(Data) error { return nil },
	}, RunParams{})
	if err == nil {
		t.Fatal("Run should return an error after a Close stanza")
	}
}

func TestRunCtxCancelCleanStop(t *testing.T) {
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	s := newFakeServer(t, srv)

	go func() {
		s.login()
		// then stay idle; the client read loop parks on ReadStanza
	}()

	c := NewConn(cli)
	clientLogin(t, c)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- c.Run(ctx, Handlers{
			OnData: func(Data) error { return nil },
		}, RunParams{})
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-runDone:
		if err != nil {
			t.Errorf("Run returned %v, want nil on ctx cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestRunDedup(t *testing.T) {
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	s := newFakeServer(t, srv)

	go func() {
		s.login()
		// same id twice, plus a fresh one
		s.writeStanza(TagDataMessageStanza, sampleData("dup", "k", "v"))
		s.writeStanza(TagDataMessageStanza, sampleData("dup", "k", "v"))
		s.writeStanza(TagDataMessageStanza, sampleData("fresh", "k", "v"))
	}()

	c := NewConn(cli)
	clientLogin(t, c)

	calls := make(chan string, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- c.Run(ctx, Handlers{
			OnData: func(d Data) error {
				pid := d.PersistentID
				calls <- pid
				return nil
			},
		}, RunParams{})
	}()

	first := <-calls
	second := <-calls
	if first != "dup" || second != "fresh" {
		t.Errorf("OnData pids = %q,%q, want dup,fresh (duplicate suppressed)", first, second)
	}
	select {
	case extra := <-calls:
		t.Errorf("OnData called a third time with %q; duplicate not suppressed", extra)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	<-runDone
}

func TestRunHandlerErrorNotAcked(t *testing.T) {
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	s := newFakeServer(t, srv)

	go func() {
		s.login()
		s.writeStanza(TagDataMessageStanza, sampleData("pid-err", "k", "v"))
		s.writeStanza(TagDataMessageStanza, sampleData("pid-ok", "k", "v"))
	}()

	c := NewConn(cli)
	clientLogin(t, c)

	calls := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- c.Run(ctx, Handlers{
			OnData: func(d Data) error {
				pid := d.PersistentID
				calls <- pid
				if pid == "pid-err" {
					return errors.New("boom")
				}
				return nil
			},
		}, RunParams{SelectiveAck: true, AbortOnSequentialErrors: 5})
	}()

	if got := <-calls; got != "pid-err" {
		t.Fatalf("first OnData pid = %q, want pid-err", got)
	}
	if got := <-calls; got != "pid-ok" {
		t.Fatalf("second OnData pid = %q, want pid-ok", got)
	}

	// A failed handler leaves its message unacked so the server can redeliver;
	// the only selective-ack on the wire is for the one that succeeded.
	tag, payload := s.readStanza()
	if tag != TagIqStanza {
		t.Fatalf("client wrote tag %d, want IqStanza", tag)
	}
	var iq pb.IqStanza
	if err := proto.Unmarshal(payload, &iq); err != nil {
		t.Fatalf("unmarshal iq: %v", err)
	}
	var sa pb.SelectiveAck
	if err := proto.Unmarshal(iq.GetExtension().GetData(), &sa); err != nil {
		t.Fatalf("unmarshal selective ack: %v", err)
	}
	if len(sa.GetId()) != 1 || sa.GetId()[0] != "pid-ok" {
		t.Errorf("selective ack ids = %v, want [pid-ok] (failed message must not be acked)", sa.GetId())
	}

	cancel()
	<-runDone
}

func TestRunDeletedMessagesControl(t *testing.T) {
	cli, srv := net.Pipe()
	t.Cleanup(func() { cli.Close(); srv.Close() })
	s := newFakeServer(t, srv)

	go func() {
		s.login()
		s.writeStanza(TagDataMessageStanza, deletedMessages("del-1"))
	}()

	c := NewConn(cli)
	clientLogin(t, c)

	var dataCalled bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- c.Run(ctx, Handlers{
			OnData: func(Data) error {
				dataCalled = true
				return nil
			},
		}, RunParams{SelectiveAck: true})
	}()

	// a deleted_messages control message gets acked but not delivered
	tag, payload := s.readStanza()
	if tag != TagIqStanza {
		t.Fatalf("expected a selective-ack IqStanza, got tag %d", tag)
	}
	var iq pb.IqStanza
	if err := proto.Unmarshal(payload, &iq); err != nil {
		t.Fatalf("unmarshal iq: %v", err)
	}
	var sa pb.SelectiveAck
	if err := proto.Unmarshal(iq.GetExtension().GetData(), &sa); err != nil {
		t.Fatalf("unmarshal selective ack: %v", err)
	}
	if len(sa.GetId()) != 1 || sa.GetId()[0] != "del-1" {
		t.Errorf("selective ack ids = %v, want [del-1]", sa.GetId())
	}
	if dataCalled {
		t.Error("OnData should not fire for a deleted_messages control message")
	}

	cancel()
	<-runDone
}

func TestRunNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()

	cli, srv := net.Pipe()
	s := newFakeServer(t, srv)
	go func() {
		s.login()
	}()

	c := NewConn(cli)
	clientLogin(t, c)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- c.Run(ctx, Handlers{
			OnData: func(Data) error { return nil },
		}, RunParams{})
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()
	<-runDone
	cli.Close()
	srv.Close()

	// give the runtime a moment to reap the read loop and monitor
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutine leak: before=%d after=%d", before, after)
	}
}
