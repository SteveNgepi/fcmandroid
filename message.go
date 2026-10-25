package fcmandroid

import "time"

// Message is one push received over MCS. Android pushes arrive in the clear.
type Message struct {
	PersistentID string // Google's message id, used for ack and dedup
	// Data is the app_data of the push: the sender's own data keys (for example
	// "sendid") next to Google's gcm.notification.* and google.c.* entries.
	Data map[string]string
	Raw  []byte // raw_data; empty for ordinary FCM pushes

	Sent        time.Time     // server send time; zero if absent
	From        string        // sender project number
	Category    string        // package name
	CollapseKey string        // collapse key of the push
	TTL         time.Duration // whole seconds; zero if absent (indistinguishable from 0 s)
}
