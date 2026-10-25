package fcmandroid

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/SteveNgepi/fcmandroid/internal/androidcheckin"
	"github.com/SteveNgepi/fcmandroid/internal/androidregister"
)

const (
	defaultHTTPTimeout = 10 * time.Second

	// deviceAttempts is how many different devices register tries when register3
	// keeps refusing one: a few checked-in ids are never accepted, however long
	// they wait (about 0.6% at 100 registrations a second), and a fresh check-in
	// gets another.
	deviceAttempts = 3
)

// Provision registers one new fake Android device for the app in cfg and
// returns it. cfg comes from [NewAppConfig]. A nil h means a client with a 10 s
// timeout.
//
// It sends live requests to Google, a checkin and a register3 call, which count
// against its limits. If register3 keeps refusing the checked-in device,
// Provision checks in again, up to three devices in all, and then returns
// [ErrPhoneRegistration]. [ErrThrottled] means an HTTP 429 survived the
// retries; any other error comes from the HTTP client or the response.
func Provision(ctx context.Context, cfg AppConfig, h *http.Client) (*Device, error) {
	if h == nil {
		h = &http.Client{Timeout: defaultHTTPTimeout}
	}
	id, err := register(ctx, cfg, h)
	if err != nil {
		return nil, err
	}
	return NewDevice(id), nil
}

// register registers one new device, checking in again as a new device if
// register3 refuses the first.
func register(ctx context.Context, cfg AppConfig, h *http.Client) (Identity, error) {
	var (
		id  Identity
		err error
	)
	for range deviceAttempts {
		id, err = newIdentity(ctx, cfg, h)
		if !errors.Is(err, androidregister.ErrPhoneRegistration) {
			break
		}
	}
	return id, err
}

// newIdentity checks in one new device, registers the app on it and returns
// the resulting identity.
func newIdentity(ctx context.Context, cfg AppConfig, h *http.Client) (Identity, error) {
	androidID, securityToken, err := androidcheckin.Do(ctx, h)
	if err != nil {
		return Identity{}, err
	}
	token, err := androidregister.Register3(ctx, h, androidID, securityToken, androidregister.App{
		Package:  cfg.Package,
		Cert:     cfg.Cert,
		SenderID: cfg.SenderID,
		AppID:    cfg.AppID,
	})
	if err != nil {
		return Identity{}, err
	}
	return Identity{Project: cfg.ProjectID, AndroidID: androidID, SecurityToken: securityToken, Token: token}, nil
}
