package retry

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const (
	// quotaWait is how long to stay away after a 429 that carries no Retry-After.
	quotaWait = time.Minute

	// maxRetryAfter caps a wait the server asks for: a buggy Retry-After must not
	// park a worker for hours.
	maxRetryAfter = 2 * quotaWait
)

// ErrThrottled marks a 429 reply.
var ErrThrottled = errors.New("throttled (HTTP 429)")

// Status classifies a reply that was not 200. It returns what a step should
// return from its attempt: 429 waits After and wraps ErrThrottled, other 4xx
// except 408 are final, anything else is retried with the usual backoff.
func Status(code int, header http.Header, err error) (time.Duration, error) {
	switch {
	case code == http.StatusTooManyRequests:
		return After(header), fmt.Errorf("%w: %w", ErrThrottled, err)
	case code >= 400 && code < 500 && code != http.StatusRequestTimeout:
		return Stop, err
	}
	return 0, err
}

// After is how long to stay away after a 429: the server's Retry-After (seconds
// or an HTTP date), capped, else a minute.
func After(h http.Header) time.Duration {
	value := h.Get("Retry-After")
	seconds, err := strconv.ParseUint(value, 10, 64)
	switch {
	case errors.Is(err, strconv.ErrRange):
		return maxRetryAfter
	case err == nil && seconds > 0:
		return time.Duration(min(seconds, uint64(maxRetryAfter/time.Second))) * time.Second
	}
	if at, parseErr := http.ParseTime(value); parseErr == nil {
		if wait := time.Until(at); wait > 0 {
			return min(wait, maxRetryAfter)
		}
	}
	return quotaWait
}
