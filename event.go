package fcmandroid

import "time"

// EventType is the kind of an [Event].
type EventType string

const (
	EventUp      EventType = "up"      // a device logged in
	EventReset   EventType = "reset"   // a device's session ended, see Event.Reason
	EventDeleted EventType = "deleted" // Google dropped the pushes it had stored for a device
	EventMsg     EventType = "msg"     // a push arrived
)

// ResetReason says why a session ended.
type ResetReason string

const (
	ResetAuth           ResetReason = "auth"
	ResetNotProvisioned ResetReason = "not_provisioned"
	ResetHeartbeat      ResetReason = "heartbeat"
	ResetServerClose    ResetReason = "server_close"
	ResetStream         ResetReason = "stream"
	ResetHandler        ResetReason = "handler"
	ResetNetwork        ResetReason = "network"
)

// Event is something that happened to a device during [Device.Listen], passed to
// [ListenOptions.OnEvent]. It marshals to one line of a JSON Lines journal.
type Event struct {
	Type  EventType `json:"type"`
	Token string    `json:"token,omitempty"` // the FCM token of the device: the address the backend sent to
	// MessageID is Google's id of a msg. A push Google redelivers after a lost ack
	// has the same one, a second send of the backend has another: only this tells
	// a duplicate of the backend from one of the connection.
	MessageID string            `json:"message_id,omitempty"`
	Reason    ResetReason       `json:"reason,omitempty"` // reset only
	Data      map[string]string `json:"data,omitempty"`   // msg only: the content of the push
	CreatedAt time.Time         `json:"created_at"`       // when the event was created; for a msg, when the push reached us
}
