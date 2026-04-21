package apiobjects

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// MakeAnyObject wraps a protobuf message into an anypb.Any. Used by the
// adapter types to produce serialized storage payloads.
func MakeAnyObject(msg proto.Message) (*anypb.Any, error) {
	anyMsg, err := anypb.New(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to create Any from protobuf message: %w", err)
	}
	return anyMsg, nil
}
