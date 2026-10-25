package mcs

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/SteveNgepi/fcmandroid/internal/pb"

	"google.golang.org/protobuf/proto"
)

// Selective-ack carries id 12; stream-ack is 13. These match the extension ids
// the server expects in an outbound IqStanza.
const (
	SelectiveAckExtensionID = 12
	StreamAckExtensionID    = 13
)

// Errors a session can end with. Callers test them with errors.Is.
var (
	// ErrAuth means the server rejected the login; the message carries its error info.
	ErrAuth = errors.New("mcs: login rejected")
	// ErrHeartbeatTimeout means a heartbeat ping was not answered in time.
	ErrHeartbeatTimeout = errors.New("mcs: heartbeat ack timed out")
	// ErrServerClose means the server sent a Close stanza.
	ErrServerClose = errors.New("mcs: server sent Close")
	// ErrStream means the server sent a stream error stanza.
	ErrStream = errors.New("mcs: stream error stanza")
	// ErrHandler means the session was aborted after repeated OnData failures;
	// the last handler error is wrapped next to it.
	ErrHandler = errors.New("mcs: aborted after repeated handler errors")
)

// sessionCache is shared by every connection: all of them go to the same host, so
// a resumed handshake skips parsing the server certificates again.
var sessionCache = tls.NewLRUClientSessionCache(64)

// Dial opens TLS to addr with default certificate verification against
// serverName, so addr may be an IP. A nil nd means a zero net.Dialer; set its
// LocalAddr to pick the source IP.
func Dial(ctx context.Context, nd *net.Dialer, addr, serverName string) (net.Conn, error) {
	d := &tls.Dialer{NetDialer: nd, Config: &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         serverName,
		ClientSessionCache: sessionCache,
	}}
	return d.DialContext(ctx, "tcp", addr)
}

// Conn is one MCS connection: login first, then Run.
type Conn struct {
	conn   net.Conn
	writer *Writer
	reader *Reader
}

// writeTimeout bounds one write. Reads have the heartbeat deadline; without
// this a blackholed connection would block the only goroutine in a ping or an ack.
const writeTimeout = 10 * time.Second

// deadlineConn gives every write a deadline.
type deadlineConn struct{ net.Conn }

func (c deadlineConn) Write(p []byte) (int, error) {
	if err := c.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

// readBufSize is small on purpose: tls.Conn already buffers a whole record, and
// payloads larger than this bypass the bufio buffer, so 4 KiB per connection
// would only duplicate it.
const readBufSize = 512

// NewConn wraps an already-dialed connection so the caller can drive login and
// the read loop over it.
func NewConn(c net.Conn) *Conn {
	return &Conn{conn: c, writer: NewWriter(deadlineConn{c}), reader: NewReader(bufio.NewReaderSize(c, readBufSize))}
}

// Close drops the underlying connection.
func (c *Conn) Close() error { return c.conn.Close() }

// Login sends the LoginRequest as the first packet (with the version byte) and
// waits for a LoginResponse. A nil return means logged in.
func (c *Conn) Login(lr *pb.LoginRequest) error {
	if err := c.writer.WriteStanza(TagLoginRequest, lr); err != nil {
		return err
	}
	if _, err := c.reader.ReadVersion(); err != nil {
		return err
	}
	tag, payload, err := c.reader.ReadStanza()
	if err != nil {
		return fmt.Errorf("mcs: read login response: %w", err)
	}
	if tag != TagLoginResponse {
		return fmt.Errorf("mcs: expected LoginResponse, got tag %d", tag)
	}
	var resp pb.LoginResponse
	if err := proto.Unmarshal(payload, &resp); err != nil {
		return fmt.Errorf("mcs: parse login response: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("%w: %v", ErrAuth, resp.GetError())
	}
	return nil
}

// Data is one data message as the server sent it.
type Data struct {
	PersistentID string
	From         string // sender project id
	Category     string // package name
	CollapseKey  string // the stanza's token field
	AppData      map[string]string
	RawData      []byte
	TTLSeconds   int32 // 0 when absent
	SentMS       int64 // server send time, Unix ms; 0 when absent
}

// Handlers are the callbacks Run makes, on its own goroutine.
type Handlers struct {
	OnData func(Data) error
	// OnDeleted is called after a deleted_messages control message is acked:
	// the server dropped stored messages for this device.
	OnDeleted func()
}

// RunParams tunes Run.
type RunParams struct {
	ClientHeartbeat         time.Duration // if >0, send a ping when idle this long
	AckTimeout              time.Duration // if a sent ping is not acked within this, reset
	SelectiveAck            bool          // send selective-acks for delivered data messages
	AbortOnSequentialErrors int           // default 3 when <=0; consecutive OnData failures that force a reset
}

// runState is owned by the single goroutine running Run, so it needs no lock.
type runState struct {
	writer            *Writer
	inputStreamID     int32
	lastStreamIDAcked int32
	lastInbound       time.Time
	awaitingAck       bool
	ackDeadline       time.Time
}

// noteInbound bumps the input stream id and records liveness. Called once per
// inbound stanza, right after a successful read, before dispatch.
func (s *runState) noteInbound() {
	s.inputStreamID++
	s.lastInbound = time.Now()
	s.awaitingAck = false
}

// stamp fills last_stream_id_received only when a new id is available, then
// advances the acked watermark. Cumulative: one id stands in for every id up to it.
func (s *runState) stamp() *int32 {
	if s.inputStreamID > s.lastStreamIDAcked {
		id := s.inputStreamID
		s.lastStreamIDAcked = id
		return new(id)
	}
	return nil
}

func (s *runState) sendAck() error {
	return s.writer.WriteStanza(TagHeartbeatAck, &pb.HeartbeatAck{LastStreamIdReceived: s.stamp()})
}

func (s *runState) sendPingAndArm(timeout time.Duration) error {
	if err := s.writer.WriteStanza(TagHeartbeatPing, &pb.HeartbeatPing{LastStreamIdReceived: s.stamp()}); err != nil {
		return err
	}
	s.awaitingAck = true
	s.ackDeadline = time.Now().Add(timeout)
	return nil
}

// readDeadline is when the read loop must wake up on its own: the ack deadline
// of an outstanding ping, else the moment the connection has been idle long
// enough to ping. Zero means block until data arrives.
func (s *runState) readDeadline(heartbeat time.Duration) time.Time {
	switch {
	case s.awaitingAck:
		return s.ackDeadline
	case heartbeat > 0:
		return s.lastInbound.Add(heartbeat)
	default:
		return time.Time{}
	}
}

func isDeletedMessages(d *pb.DataMessageStanza) bool {
	for _, kv := range d.GetAppData() {
		if kv.GetKey() == "message_type" && kv.GetValue() == "deleted_messages" {
			return true
		}
	}
	return false
}

// Run drives the read loop and the heartbeat on the calling goroutine until ctx
// is cancelled (clean stop, returns nil) or a reset condition fires (returns
// that error). The heartbeat rides on read deadlines, so a connection costs one
// goroutine and no ticker.
func (c *Conn) Run(ctx context.Context, h Handlers, p RunParams) error {
	abortAt := p.AbortOnSequentialErrors
	if abortAt <= 0 {
		abortAt = 3
	}

	// The LoginResponse was already read in Login; the server counts it as the
	// first inbound stanza of the stream, so the input stream id starts at 1.
	// Seeding it here keeps the cumulative last_stream_id_received ack aligned
	// with the server instead of trailing by one.
	st := &runState{writer: c.writer, lastInbound: time.Now(), inputStreamID: 1}

	// A read parked in ReadStanza only wakes on data, a deadline or a closed
	// conn, so cancellation closes the conn.
	stop := context.AfterFunc(ctx, func() { _ = c.conn.Close() }) // best effort: the session is ending
	defer stop()

	err := c.readLoop(h, p, st, abortAt)
	_ = c.conn.Close() // best effort: the session is over, its error is the one that matters
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (c *Conn) readLoop(h Handlers, p RunParams, st *runState, abortAt int) error {
	var seen map[string]struct{}
	var notifyErrors int
	for {
		if err := c.conn.SetReadDeadline(st.readDeadline(p.ClientHeartbeat)); err != nil {
			return fmt.Errorf("mcs: set read deadline: %w", err)
		}
		tag, payload, err := c.reader.ReadStanza()
		if err != nil {
			var mid midStanzaError
			if errors.As(err, &mid) || !errors.Is(err, os.ErrDeadlineExceeded) {
				return fmt.Errorf("mcs: read stanza: %w", err)
			}
			if st.awaitingAck {
				return ErrHeartbeatTimeout
			}
			if err := st.sendPingAndArm(p.AckTimeout); err != nil {
				return fmt.Errorf("mcs: send ping: %w", err)
			}
			continue
		}
		st.noteInbound()

		switch tag {
		case TagHeartbeatPing:
			if err := st.sendAck(); err != nil {
				return fmt.Errorf("mcs: send heartbeat ack: %w", err)
			}
		case TagHeartbeatAck:
			// liveness already recorded by noteInbound
		case TagLoginResponse:
			// only meaningful at handshake; ignore mid-stream
		case TagDataMessageStanza:
			var dm pb.DataMessageStanza
			if err := proto.Unmarshal(payload, &dm); err != nil {
				return fmt.Errorf("mcs: parse data message: %w", err)
			}
			pid := dm.GetPersistentId()
			// An empty id cannot be deduplicated or acked; deliver it every time.
			if _, dup := seen[pid]; dup && pid != "" {
				// already handled this session: ack again so the server stops
				// resending, but do not deliver it a second time
				if err := c.ackIfEnabled(st, p, pid); err != nil {
					return err
				}
				continue
			}

			if isDeletedMessages(&dm) {
				// control message: no payload, no OnData. Mark handled and
				// ack so the server stops resending it.
				seen = markSeen(seen, pid)
				if err := c.ackIfEnabled(st, p, pid); err != nil {
					return err
				}
				if h.OnDeleted != nil {
					h.OnDeleted()
				}
				continue
			}

			appData := make(map[string]string, len(dm.GetAppData()))
			for _, kv := range dm.GetAppData() {
				appData[kv.GetKey()] = kv.GetValue()
			}
			if h.OnData != nil {
				if err := h.OnData(Data{
					PersistentID: pid,
					From:         dm.GetFrom(),
					Category:     dm.GetCategory(),
					CollapseKey:  dm.GetToken(),
					AppData:      appData,
					RawData:      dm.GetRawData(),
					TTLSeconds:   dm.GetTtl(),
					SentMS:       dm.GetSent(),
				}); err != nil {
					notifyErrors++
					if notifyErrors >= abortAt {
						return fmt.Errorf("%w (%d in a row): %w", ErrHandler, notifyErrors, err)
					}
					// leave it unmarked and unacked so the server can redeliver
					continue
				}
				notifyErrors = 0
			}
			seen = markSeen(seen, pid)
			if err := c.ackIfEnabled(st, p, pid); err != nil {
				return err
			}
		case TagClose:
			return ErrServerClose
		case TagStreamErrorStanza:
			return ErrStream
		default:
			// an unknown stanza is skipped; it still counted as inbound
		}
	}
}

// maxSeen bounds the dedup set of a session. The set only catches a redelivery
// within the same session, which comes soon after the first delivery, so
// forgetting everything once the set is full costs next to nothing.
const maxSeen = 1024

// markSeen records pid, allocating the set on first use: most connections
// never receive a message, so they never pay for the map.
func markSeen(seen map[string]struct{}, pid string) map[string]struct{} {
	if pid == "" {
		return seen
	}
	if seen == nil {
		seen = make(map[string]struct{})
	}
	if len(seen) >= maxSeen {
		clear(seen)
	}
	seen[pid] = struct{}{}
	return seen
}

func (c *Conn) ackIfEnabled(st *runState, p RunParams, pid string) error {
	if !p.SelectiveAck || pid == "" {
		return nil
	}
	iq, err := buildSelectiveAck(pid)
	if err != nil {
		return fmt.Errorf("mcs: build selective ack: %w", err)
	}
	if err := st.writer.WriteStanza(TagIqStanza, iq); err != nil {
		return fmt.Errorf("mcs: send selective ack: %w", err)
	}
	return nil
}

func buildSelectiveAck(pid string) (*pb.IqStanza, error) {
	sa := &pb.SelectiveAck{Id: []string{pid}}
	data, err := proto.Marshal(sa)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	set := pb.IqStanza_SET
	return &pb.IqStanza{
		Id:        new(""),
		Type:      &set,
		Extension: &pb.Extension{Id: proto.Int32(SelectiveAckExtensionID), Data: data},
	}, nil
}
