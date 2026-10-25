package fcmandroid

import (
	"errors"

	"github.com/SteveNgepi/fcmandroid/internal/androidregister"
	"github.com/SteveNgepi/fcmandroid/internal/mcs"
	"github.com/SteveNgepi/fcmandroid/internal/retry"
)

// ErrNotProvisioned is returned by Listen when the identity has no android id or
// no security token: Listen never registers on its own.
var ErrNotProvisioned = errors.New("fcmandroid: identity is not provisioned")

// ErrAlreadyListening is returned by Device.Listen when the device already has a
// session: a device holds at most one connection.
var ErrAlreadyListening = errors.New("fcmandroid: device is already listening")

// Errors of Listen. Test them with errors.Is; dial and TLS errors are returned
// as the underlying error.
var (
	// ErrAuth: the server rejected the login. The message keeps its error info.
	ErrAuth = mcs.ErrAuth
	// ErrHeartbeatTimeout: a heartbeat ping got no answer in time.
	ErrHeartbeatTimeout = mcs.ErrHeartbeatTimeout
	// ErrServerClose: the server sent a Close stanza.
	ErrServerClose = mcs.ErrServerClose
	// ErrStream: the server sent a stream error stanza.
	ErrStream = mcs.ErrStream
	// ErrHandler: the session was aborted after repeated OnMessage failures; the
	// callback's last error is wrapped too.
	ErrHandler = mcs.ErrHandler
)

// Errors of provisioning.
var (
	// ErrPhoneRegistration: register3 answered Error=PHONE_REGISTRATION_ERROR on
	// every attempt. Right after a checkin the answer is normal for about 2 s and
	// the step retries it; what reaches the caller persisted for several seconds.
	ErrPhoneRegistration = androidregister.ErrPhoneRegistration
	// ErrThrottled: an HTTP 429 survived the retries.
	ErrThrottled = retry.ErrThrottled
)
