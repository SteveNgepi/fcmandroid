package mcs

import (
	"strconv"

	"github.com/SteveNgepi/fcmandroid/internal/pb"

	"google.golang.org/protobuf/proto"
)

// LoginConfig is what BuildLoginRequest needs.
type LoginConfig struct {
	AndroidID     uint64
	SecurityToken uint64
	ClientID      string // LoginRequest.id; the server accepts any value, pushswarm sends a Chrome version
	HeartbeatMS   int    // server heartbeat interval ms; <=0 omits heartbeat_stat
}

// BuildLoginRequest returns the first packet of a session for the device in c.
func BuildLoginRequest(c LoginConfig) *pb.LoginRequest {
	androidStr := strconv.FormatUint(c.AndroidID, 10)
	authService := pb.LoginRequest_ANDROID_ID
	lr := &pb.LoginRequest{
		Id:                new(c.ClientID),
		Domain:            new("mcs.android.com"),
		User:              new(androidStr),
		Resource:          new(androidStr),
		AuthToken:         new(strconv.FormatUint(c.SecurityToken, 10)),
		DeviceId:          new("android-" + strconv.FormatUint(c.AndroidID, 16)),
		AuthService:       &authService,
		NetworkType:       proto.Int32(1),
		UseRmq2:           new(true),
		AdaptiveHeartbeat: new(false),
		Setting:           []*pb.Setting{{Name: new("new_vc"), Value: new("1")}},
	}
	if c.HeartbeatMS > 0 {
		lr.HeartbeatStat = &pb.HeartbeatStat{
			Ip:         new(""),
			Timeout:    new(true),
			IntervalMs: new(int32(c.HeartbeatMS)),
		}
	}
	return lr
}
