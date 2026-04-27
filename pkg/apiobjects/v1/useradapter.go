package apiobjects

import (
	"fmt"

	"github.com/tlalocweb/hulation/pkg/store/common"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type UserAdapter struct {
	user *User
	common.DefaultIndexHandler
	common.DeepCopyHandler
}

func SerWrapUser(user *User) *UserAdapter {
	ret := &UserAdapter{
		user: user,
	}
	ret.Self = ret
	return ret
}

func (ua *UserAdapter) Finalize() error {
	if ua.user == nil {
		return fmt.Errorf("user is nil")
	}
	if ua.user.Apiver == "" {
		ua.user.Apiver = APIOBJECTS_VER
	}
	if ua.user.Uuid == "" {
		return fmt.Errorf("user uuid is empty")
	}
	now := timestamppb.Now()
	if ua.user.CreatedAt == nil {
		ua.user.CreatedAt = now
	}
	ua.user.UpdatedAt = now
	return nil
}

// func (ua *UserAdapter) GetProtoMsg() (proto.Message, bool) {
// 	return ua.user, true
// }

func (ua *UserAdapter) GetSerialData() (ret []byte, err error) {
	var anyobj *anypb.Any
	anyobj, err = MakeAnyObject(ua.user)
	if err != nil {
		return nil, fmt.Errorf("error creating 'any' for protobuf: %v", err)
	}
	ret, err = proto.Marshal(anyobj)
	if err != nil {
		return nil, fmt.Errorf("error marshalling protobuf: %v", err)
	}
	return
}

// NOTE: These methods are commented out as the underlying fields no longer exist in the new protobuf schema
// If token management needs to be reimplemented, consider adding these fields back or using a different approach

// func (u *User) UpdateLastTokenLookup() {
// 	u.LastLoginAt = timestamppb.Now()
// }

// func (u *User) UpdateGoodTokenVersion() {
// 	// Field removed from protobuf - token versioning may need different approach
// }
