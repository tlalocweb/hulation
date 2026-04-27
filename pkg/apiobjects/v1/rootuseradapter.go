package apiobjects

import (
	"fmt"
	"time"

	"github.com/tlalocweb/hulation/pkg/store/common"
	"github.com/tlalocweb/hulation/pkg/utils"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const (
	MAX_TOKEN_VERSIONS_VALID = 8
)

type RootUserAdapter struct {
	user *RootUser
	common.DefaultIndexHandler
	common.DeepCopyHandler
}

func SerWrapRootUser(user *RootUser) *RootUserAdapter {
	ret := &RootUserAdapter{
		user: user,
	}
	ret.Self = ret
	return ret
}

// func (ua *UserAdapter) Serialize() ([]byte, error) {
// 	if ua.user == nil {
// 		return nil, fmt.Errorf("user is nil")
// 	}
// 	if ua.user.Ver == "" {
// 		ua.user.Ver = "v1"
// 	}
// 	if ua.user.Uuid == "" {
// 		return nil, fmt.Errorf("user uuid is empty")
// 	}
// 	now := time.Now().UnixNano()
// 	if ua.user.Created == 0 {
// 		ua.user.Created = now
// 	}
// 	ua.user.Updated = now
// 	return proto.Marshal(ua.user)
// }

func (ua *RootUserAdapter) Finalize() error {
	if ua.user == nil {
		return fmt.Errorf("rootuser is nil")
	}
	if ua.user.Apiver == "" {
		ua.user.Apiver = APIOBJECTS_VER
	}
	now := time.Now().UnixNano()
	if ua.user.Created == 0 {
		ua.user.Created = now
	}
	ua.user.Updated = now
	return nil
}

// func (ua *UserAdapter) GetProtoMsg() (proto.Message, bool) {
// 	return ua.user, true
// }

func (ua *RootUserAdapter) GetSerialData() (ret []byte, err error) {
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

func (ua *RootUserAdapter) UpdateLastTokenVersion() {

	ua.user.LastTokenVersion++
}

func (ua *RootUserAdapter) GetLastGoodTokenVersion() int64 {
	return ua.user.GoodTokenVersion
}

func (ua *RootUserAdapter) InvalidateTokensFromVersion(version int64) {
	ua.user.GoodTokenVersion = version + 1
}

func InitRootUser() *RootUser {
	return &RootUser{
		Apiver: APIOBJECTS_VER,
	}
}

// the passed in hash is the network-hash password (we never send the actual password
// across the network - it would be hashed with sha256 or similar. The choice of hash there
// is a question for the API layer) the user's hash is the hashed password in the database
// (which uses argon2). Returns true if the 'hash' is the same RootUser's hash in the db
func (u *RootUser) CheckHashedPassword(networkhash string) bool {
	ok, err := utils.Argon2CompareHashAndSecret(u.Hash, networkhash)
	if err != nil {
		log.Errorf("error comparing password hashes: %v", err)
		return false
	}
	return ok
}
