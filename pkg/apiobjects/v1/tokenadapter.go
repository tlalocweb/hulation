package apiobjects

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tlalocweb/hulation/pkg/store/common"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

type TokenAdapter struct {
	t *StoredToken
	common.DefaultIndexHandler
	common.DeepCopyHandler
}

func SerWrapToken(tok *StoredToken) *TokenAdapter {
	ret := &TokenAdapter{
		t: tok,
	}
	ret.Self = ret
	return ret
}

func (a *TokenAdapter) Finalize() error {
	if len(a.t.Jwt) < 1 {
		return fmt.Errorf("jwt is empty")
	}
	now := time.Now().UnixNano()
	if a.t.Created == 0 {
		a.t.Created = now
	}
	return nil
}

func (a *TokenAdapter) GetSerialData() (ret []byte, err error) {
	var anyobj *anypb.Any
	anyobj, err = MakeAnyObject(a.t)
	if err != nil {
		return nil, fmt.Errorf("error creating 'any' for protobuf: %v", err)
	}
	ret, err = proto.Marshal(anyobj)
	if err != nil {
		return nil, fmt.Errorf("error marshalling protobuf: %v", err)
	}
	return
}

type TokenKeyAdapter struct {
	t *StoredTokenKey
	common.DefaultIndexHandler
	common.DeepCopyHandler
}

func SerWrapTokenKey(tok *StoredTokenKey) *TokenKeyAdapter {
	ret := &TokenKeyAdapter{
		t: tok,
	}
	ret.Self = ret
	return ret
}

func (a *TokenKeyAdapter) Finalize() error {
	if len(a.t.Key) < 1 {
		return fmt.Errorf("key is empty")
	}
	now := time.Now().UnixNano()
	if a.t.Created == 0 {
		a.t.Created = now
	}
	return nil
}

func (a *TokenKeyAdapter) GetSerialData() (ret []byte, err error) {
	var anyobj *anypb.Any
	anyobj, err = MakeAnyObject(a.t)
	if err != nil {
		return nil, fmt.Errorf("error creating 'any' for protobuf: %v", err)
	}
	ret, err = proto.Marshal(anyobj)
	if err != nil {
		return nil, fmt.Errorf("error marshalling protobuf: %v", err)
	}
	return
}

type SessionAdapter struct {
	s *Session
	common.DefaultIndexHandler
	common.DeepCopyHandler
}

func SerWrapSession(sess *Session) *SessionAdapter {
	ret := &SessionAdapter{
		s: sess,
	}
	ret.Self = ret
	return ret
}

func (a *SessionAdapter) Finalize() error {
	if a.s.UserId == "" {
		return fmt.Errorf("user uuid is empty")
	}
	if a.s.SessionId == "" {
		return fmt.Errorf("session id is empty")
	}
	if a.s.TokenId == "" {
		return fmt.Errorf("token is empty")
	}
	now := time.Now().UnixNano()
	if a.s.Created == 0 {
		a.s.Created = now
	}
	return nil
}

func (a *SessionAdapter) GetSerialData() (ret []byte, err error) {
	var anyobj *anypb.Any
	anyobj, err = MakeAnyObject(a.s)
	if err != nil {
		return nil, fmt.Errorf("error creating 'any' for protobuf: %v", err)
	}
	ret, err = proto.Marshal(anyobj)
	if err != nil {
		return nil, fmt.Errorf("error marshalling protobuf: %v", err)
	}
	return
}

func NewSessionForRoot() (ret *SessionAdapter) {
	ret = &SessionAdapter{
		s: &Session{
			UserId:    "root",
			SessionId: uuid.New().String(),
		},
	}
	return
}

func NewSessionForUser(user *User) (ret *SessionAdapter) {
	ret = &SessionAdapter{
		s: &Session{
			UserId:    user.Uuid,
			SessionId: uuid.New().String(),
		},
	}
	return
}

// NewSessionForRegistryUser (izcr-only) intentionally omitted: hulation
// has no OCI registry user concept.

func (a *SessionAdapter) GetSessionId() string {
	return a.s.SessionId
}

func (a *SessionAdapter) AddToken(id string) {
	a.s.TokenId = id
}
