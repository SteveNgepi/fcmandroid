package fcmandroid

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/SteveNgepi/fcmandroid/internal/mcs"
)

// State is where a Device is in its session with FCM.
type State int32

// The states of a Device. Listen moves it Offline → Connecting → Online and
// back to Offline when the session ends, whatever the reason.
const (
	StateOffline    State = iota // no session; the zero value, so a new Device is offline
	StateConnecting              // Listen is dialing and logging in
	StateOnline                  // the login succeeded, pushes are delivered
)

// Identity is a fake Android device: what Listen logs in with and the token a
// sender addresses.
type Identity struct {
	Project       string `json:"project"`
	AndroidID     uint64 `json:"android_id"`
	SecurityToken uint64 `json:"security_token"`
	Token         string `json:"token"` // FCM token, the address to send to
}

// Device is one fake Android phone registered with Google. It holds the data
// that makes it addressable and at most one session with FCM at a time. Use it
// through a pointer: it must not be copied, and its methods are safe to call
// from several goroutines.
type Device struct {
	identity Identity
	state    atomic.Int32 // a State; the zero value is StateOffline
}

// NewDevice returns an offline Device for an already registered identity, for
// example one read back from storage. It talks to nobody; see Provision to
// register a new device.
func NewDevice(identity Identity) *Device {
	return &Device{identity: identity}
}

// Token returns the FCM token, the address a sender pushes to.
func (d *Device) Token() string {
	return d.identity.Token
}

// Identity returns a copy of the device's registration data, for saving it and
// restoring the device with NewDevice.
func (d *Device) Identity() Identity {
	return d.identity
}

// State returns the current state of the device's session.
func (d *Device) State() State {
	return State(d.state.Load())
}

// setState records the state of the session; only Listen changes it.
func (d *Device) setState(state State) {
	d.state.Store(int32(state))
}

// Listen logs the device in and delivers its pushes until ctx is cancelled
// (nil) or the session fails (the error). A device holds one session at a time:
// a call while another one runs returns ErrAlreadyListening. It never
// reconnects: retries, backoff and rate limits belong to the caller.
func (d *Device) Listen(ctx context.Context, opt ListenOptions) error {
	if !d.state.CompareAndSwap(int32(StateOffline), int32(StateConnecting)) {
		return ErrAlreadyListening
	}
	defer d.setState(StateOffline)

	emit := func(e Event) {
		if opt.OnEvent != nil {
			opt.OnEvent(e)
		}
	}

	onConnect := opt.OnConnect
	opt.OnConnect = func() {
		d.setState(StateOnline)
		emit(Event{Type: EventUp, Token: d.Token(), CreatedAt: time.Now()})
		if onConnect != nil {
			onConnect()
		}
	}

	onMessage := opt.OnMessage
	opt.OnMessage = func(m Message) error {
		emit(Event{Type: EventMsg, Token: d.Token(), CreatedAt: time.Now(), MessageID: m.PersistentID, Data: m.Data})
		if onMessage != nil {
			return onMessage(m)
		}
		return nil
	}

	onDeleted := opt.OnDeleted
	opt.OnDeleted = func() {
		emit(Event{Type: EventDeleted, Token: d.Token(), CreatedAt: time.Now()})
		if onDeleted != nil {
			onDeleted()
		}
	}

	err := listen(ctx, d.identity, opt, mcs.Dial)
	if err != nil {
		emit(Event{Type: EventReset, Token: d.Token(), CreatedAt: time.Now(), Reason: reasonOf(err)})
	}
	return err
}

// reasonOf names why a session ended, for the reset events.
func reasonOf(err error) ResetReason {
	switch {
	case errors.Is(err, ErrAuth):
		return ResetAuth
	case errors.Is(err, ErrNotProvisioned):
		return ResetNotProvisioned
	case errors.Is(err, ErrHeartbeatTimeout):
		return ResetHeartbeat
	case errors.Is(err, ErrServerClose):
		return ResetServerClose
	case errors.Is(err, ErrStream):
		return ResetStream
	case errors.Is(err, ErrHandler):
		return ResetHandler
	}
	return ResetNetwork
}
