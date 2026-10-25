// Package androidregister performs the GCM registration of an Android app, which
// returns the token a sender addresses.
package androidregister

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SteveNgepi/fcmandroid/internal/retry"
)

// phoneRetryDelay is how long to wait before asking register3 again after
// PHONE_REGISTRATION_ERROR: a device checked in a moment ago is not accepted for
// about 2 s (measured: refused at +0.1 s, accepted at +2 s, 6 of 6 times).
const phoneRetryDelay = 2 * time.Second

// ErrPhoneRegistration means register3 answered Error=PHONE_REGISTRATION_ERROR
// on every attempt. A missing or wrong certificate gives it too, and so may
// throttling; it is not known to be a final config error.
var ErrPhoneRegistration = errors.New("device not accepted yet (PHONE_REGISTRATION_ERROR)")

// transientErrors are the Error= codes of the GCM registration reply that mean
// "try again" (from the documented GCM registration errors).
var transientErrors = map[string]bool{
	"SERVICE_NOT_AVAILABLE": true,
	"INTERNAL_SERVER_ERROR": true,
}

var errNoToken = errors.New("no token in body")

// App identifies the Android app the device pretends to run.
type App struct {
	Package  string // Android package name
	Cert     string // SHA-1 of the signing certificate, lower-case hex without colons
	SenderID string // project number
	AppID    string // Firebase app id, "1:NNN:android:..."
}

const endpoint = "https://android.clients.google.com/c2dm/register3"

// Register3 registers the checked-in device for app and returns its token.
func Register3(ctx context.Context, h *http.Client, androidID, securityToken uint64, app App) (string, error) {
	form := url.Values{
		"app":          {app.Package},
		"cert":         {app.Cert},
		"sender":       {app.SenderID},
		"X-subtype":    {app.SenderID},
		"device":       {strconv.FormatUint(androidID, 10)},
		"X-gms_app_id": {app.AppID},
		"app_ver":      {"1"},
		"X-app_ver":    {"1"},
		"X-osv":        {"29"},
		"X-cliv":       {"fiid-21.1.1"},
		"X-gmsv":       {"220217001"},
		"X-scope":      {"*"},
	}
	var token string
	err := retry.Do(ctx, func() (time.Duration, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return retry.Stop, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", fmt.Sprintf("AidLogin %d:%d", androidID, securityToken))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		body, wait, err := send(h, req)
		if err != nil {
			return wait, err
		}
		s := strings.TrimSpace(string(body))
		if s == "Error=PHONE_REGISTRATION_ERROR" {
			return phoneRetryDelay, ErrPhoneRegistration
		}
		if code, isError := strings.CutPrefix(s, "Error="); isError { // a 200 can still carry Error=...
			if transientErrors[code] {
				return 0, errors.New(s)
			}
			return retry.Stop, errors.New(s) // a wrong sender or package stays wrong
		}
		t, ok := strings.CutPrefix(s, "token=")
		if !ok || t == "" {
			return 0, errNoToken
		}
		token = t
		return 0, nil
	})
	if err != nil {
		return "", fmt.Errorf("register3: %w", err)
	}
	return token, nil
}

// send does one exchange and returns the body of a 200 reply. On failure it also
// returns how long retry.Do should wait before the next attempt.
func send(h *http.Client, req *http.Request) ([]byte, time.Duration, error) {
	resp, err := h.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		wait, statusErr := retry.Status(resp.StatusCode, resp.Header, fmt.Errorf("status %d: %.200s", resp.StatusCode, body))
		return nil, wait, statusErr
	}
	return body, 0, nil
}
