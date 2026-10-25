// Package androidcheckin registers a new fake Android device with Google and
// returns its android id and security token, the login for MCS and register3.
package androidcheckin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/SteveNgepi/fcmandroid/internal/pb"
	"github.com/SteveNgepi/fcmandroid/internal/retry"

	"google.golang.org/protobuf/proto"
)

const endpoint = "https://android.clients.google.com/checkin"

// errNoIdentity means the reply parsed but carried no android id or security token.
var errNoIdentity = errors.New("response missing android id or security token")

// Do run the check-in of a new device.
func Do(ctx context.Context, h *http.Client) (androidID, securityToken uint64, err error) {
	body, err := proto.Marshal(buildRequest())
	if err != nil {
		return 0, 0, fmt.Errorf("checkin: marshal request: %w", err)
	}
	err = retry.Do(ctx, func() (time.Duration, error) {
		out, wait, postErr := post(ctx, h, body)
		if postErr != nil {
			return wait, postErr
		}
		androidID, securityToken = out.GetAndroidId(), out.GetSecurityToken()
		if androidID == 0 || securityToken == 0 {
			return 0, errNoIdentity
		}
		return 0, nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("checkin: %w", err)
	}
	return androidID, securityToken, nil
}

// post sends one check-in request. On failure it also returns how long
// retry.Do should wait before the next attempt.
func post(ctx context.Context, h *http.Client, body []byte) (*pb.AndroidCheckinResponse, time.Duration, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, retry.Stop, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := h.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		wait, statusErr := retry.Status(resp.StatusCode, resp.Header, fmt.Errorf("unexpected status %d", resp.StatusCode))
		return nil, wait, statusErr
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read response: %w", err)
	}
	var out pb.AndroidCheckinResponse
	if err = proto.Unmarshal(raw, &out); err != nil {
		return nil, 0, fmt.Errorf("parse response: %w", err)
	}
	return &out, 0, nil
}

func buildRequest() *pb.AndroidCheckinRequest {
	deviceType := pb.DeviceType_DEVICE_ANDROID_OS
	version := int32(3)
	userSerial := int32(0)

	return &pb.AndroidCheckinRequest{
		UserSerialNumber: &userSerial,
		Version:          &version,
		Locale:           new("en_US"),
		TimeZone:         new("GMT"),
		Checkin:          &pb.AndroidCheckinProto{Type: &deviceType},
	}
}
