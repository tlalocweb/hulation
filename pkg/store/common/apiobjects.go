package common

import (
	"fmt"

	"github.com/tlalocweb/hulation/log"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
)

type Unwrappable struct {
	data         []byte
	obj          protoreflect.ProtoMessage
	rewrappedobj SerialAdapter
}

func (u *Unwrappable) Unwrap() (err error) {
	if u.data == nil {
		return nil
	}
	anyobj := &anypb.Any{}
	err = proto.Unmarshal(u.data, anyobj)
	if err != nil {
		return fmt.Errorf("error unmarshalling protobuf from store: %v", err)
	}
	u.obj, err = anyobj.UnmarshalNew()
	if err != nil {
		return fmt.Errorf("error unmarshalling protobuf from 'any': %v", err)
	}
	return
}

func (u *Unwrappable) Rewrap(obj SerialAdapter) (err error) {
	u.rewrappedobj = obj
	// err = obj.Finalize()
	// if err != nil {
	// 	return fmt.Errorf("error finalizing object: %v", err)
	// }
	// var anyobj *anypb.Any
	// p, ok := obj.GetProtoMsg()
	// if ok {
	// 	anyobj, err = anypb.New(p)
	// 	if err != nil {
	// 		return fmt.Errorf("error creating 'any' for protobuf: %v", err)
	// 	}
	// } else {
	// 	return fmt.Errorf("error getting proto message from object")
	// }
	// u.data, err = proto.Marshal(anyobj)
	// if err != nil {
	// 	return fmt.Errorf("error marshalling protobuf: %v", err)
	// }
	return
}

func (u *Unwrappable) Obj() interface{} {
	return u.obj
}

func NewUnwrappable(data []byte) *Unwrappable {
	return &Unwrappable{
		data:         data,
		rewrappedobj: nil,
		obj:          nil,
	}
}

func NewUnwrappableFromObj(obj SerialAdapter) *Unwrappable {
	return &Unwrappable{
		data:         nil,
		rewrappedobj: obj,
		obj:          nil,
	}
}

func (u *Unwrappable) Finalize() error {
	if u.rewrappedobj != nil {
		log.Debugf("finalizing rewrapped object")
		return u.rewrappedobj.Finalize()
	}
	return nil
}

// func (u *Unwrappable) GetProtoMsg() (proto.Message, bool) {
// 	if u.unwrapped {
// 		if u.rewrappedobj != nil {
// 			return u.rewrappedobj.GetProtoMsg()
// 		}
// 	}
// 	return nil, false
// }

func (u *Unwrappable) GetSerialData() ([]byte, error) {
	if u.rewrappedobj != nil {
		log.Debugf("getting serialized rewrapped object")
		return u.rewrappedobj.GetSerialData()
	}
	return u.data, nil
}

func (u *Unwrappable) GetCollectionBase() string {
	if u.rewrappedobj != nil {
		return u.rewrappedobj.GetCollectionBase()
	}
	return ""
}

func (u *Unwrappable) GetIndexes() map[string]*Index {
	if u.rewrappedobj != nil {
		return u.rewrappedobj.GetIndexes()
	}
	//log.Errorf("error getting indexes - underlying SerialAdapter is nil")
	return nil
}

func (u *Unwrappable) GetDeleteIndexes() map[string]*Index {
	if u.rewrappedobj != nil {
		return u.rewrappedobj.GetDeleteIndexes()
	}
	//log.Errorf("error getting delete indexes - underlying SerialAdapter is nil")
	return nil
}

func (u *Unwrappable) GetCollectionKey() string {
	return u.rewrappedobj.GetCollectionKey()
}
