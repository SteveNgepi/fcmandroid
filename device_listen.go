package fcmandroid

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"time"

	"github.com/SteveNgepi/fcmandroid/internal/mcs"
	"github.com/SteveNgepi/fcmandroid/internal/pb"
)

const (
	// loginID is the client id sent in the MCS login request. It is a Chrome
	// version carried over from the web client; Google accepts it from Android
	// devices too (verified with live pushes).
	loginID = "94.0.4606.51"

	mcsHost = "mtalk.google.com"
	mcsPort = "5228"
)

const (
	serverHeartbeat  = 10 * time.Second // heartbeat interval announced to the server at login
	defaultHeartbeat = 20 * time.Second // client ping after this much idle time
	minHeartbeat     = time.Second      // floor: a smaller value would ping in a tight loop
	ackTimeout       = 5 * time.Second  // how long a ping may stay unanswered
	loginTimeout     = 30 * time.Second
	dialTimeout      = 30 * time.Second // TCP connect and TLS handshake together
	abortAfterErrors = 3                // consecutive OnMessage failures that end the session
)

// ListenOptions configures one Listen session.
type ListenOptions struct {
	// Dialer opens the TCP connection; set its LocalAddr to pick the source IP.
	// Nil means a zero net.Dialer.
	Dialer *net.Dialer
	// OnConnect is called once the login succeeded.
	OnConnect func()
	// OnMessage is called synchronously for each push. An error leaves the push
	// unacked by its persistent id, so Google should redeliver it, but only on a
	// later session: not within this one (not verified against a live session).
	// Three failures in a row end the session with ErrHandler. A panic in any
	// callback is not recovered.
	OnMessage func(Message) error
	// OnDeleted is called when Google reports deleted_messages: it discarded
	// pushes it stored for this device, so some pushes were lost for good. The
	// control message is acked either way.
	OnDeleted func()
	// OnEvent is called synchronously for each Event of the session. It must not
	// block: the read loop waits for it. A panic in it is not recovered.
	OnEvent func(Event)
	// Addr is the MCS address to dial, "host:port"; empty → mtalk.google.com:5228.
	// The TLS name stays mtalk.google.com, so Addr may be an IP.
	Addr string
	// Heartbeat is how long the connection may stay idle before a ping; 0 → 20 s,
	// anything below 1 s is raised to 1 s.
	Heartbeat time.Duration
}

type dialFunc func(ctx context.Context, d *net.Dialer, addr, serverName string) (net.Conn, error)

func listen(ctx context.Context, id Identity, opt ListenOptions, dial dialFunc) error {
	if id.AndroidID == 0 || id.SecurityToken == 0 {
		return ErrNotProvisioned
	}

	dialCtx, cancelDial := context.WithTimeout(ctx, dialTimeout)
	rawConn, err := dial(dialCtx, opt.Dialer, cmp.Or(opt.Addr, mcsHost+":"+mcsPort), mcsHost)
	cancelDial()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("dial: %w", err)
	}
	conn := mcs.NewConn(rawConn)
	defer func() { _ = conn.Close() }() // best effort: Run closes it too, a second Close is harmless; this also covers a panicking callback
	lr := mcs.BuildLoginRequest(mcs.LoginConfig{
		AndroidID:     id.AndroidID,
		SecurityToken: id.SecurityToken,
		ClientID:      loginID,
		HeartbeatMS:   int(serverHeartbeat / time.Millisecond),
	})
	if err := login(ctx, rawConn, conn, lr); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("login: %w", err)
	}
	if opt.OnConnect != nil {
		opt.OnConnect()
	}

	var onData func(mcs.Data) error
	if opt.OnMessage != nil {
		onData = func(d mcs.Data) error {
			m := Message{
				PersistentID: d.PersistentID,
				Data:         d.AppData,
				Raw:          d.RawData,
				From:         d.From,
				Category:     d.Category,
				CollapseKey:  d.CollapseKey,
				TTL:          time.Duration(d.TTLSeconds) * time.Second,
			}
			if d.SentMS > 0 {
				m.Sent = time.UnixMilli(d.SentMS)
			}
			return opt.OnMessage(m)
		}
	}
	return conn.Run(ctx, mcs.Handlers{OnData: onData, OnDeleted: opt.OnDeleted}, mcs.RunParams{
		ClientHeartbeat:         heartbeatOrDefault(opt.Heartbeat),
		AckTimeout:              ackTimeout,
		SelectiveAck:            true,
		AbortOnSequentialErrors: abortAfterErrors,
	})
}

func heartbeatOrDefault(d time.Duration) time.Duration {
	if d <= 0 {
		return defaultHeartbeat
	}
	return max(d, minHeartbeat)
}

// login bounds the handshake: a server that accepts TCP and then stays silent
// would otherwise park Login forever, and cancelling ctx must abort it too.
func login(ctx context.Context, rawConn net.Conn, conn *mcs.Conn, lr *pb.LoginRequest) error {
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() }) // best effort: the session is ending
	defer stop()
	if err := rawConn.SetDeadline(time.Now().Add(loginTimeout)); err != nil {
		return err
	}
	if err := conn.Login(lr); err != nil {
		return err
	}
	return rawConn.SetDeadline(time.Time{})
}
