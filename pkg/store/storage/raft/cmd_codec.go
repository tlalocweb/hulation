package raftbackend

// Helpers around the protobuf-generated cmd.pb.go. We keep the
// (de)serialise + tiny constructor helpers in a hand-rolled file
// so cmd.pb.go can be regenerated freely without churn here.

import (
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/tlalocweb/hulation/pkg/store/storage"
)

// encodeCommand marshals a Command into bytes for raft.Apply.
func encodeCommand(cmd *Command) ([]byte, error) {
	if cmd == nil {
		return nil, errors.New("raftcmd: nil command")
	}
	return proto.Marshal(cmd)
}

// decodeCommand unmarshals a raft log entry payload back into a
// Command. Returns a wrapped error so the FSM can surface the
// underlying decode failure cleanly.
func decodeCommand(data []byte) (*Command, error) {
	cmd := &Command{}
	if err := proto.Unmarshal(data, cmd); err != nil {
		return nil, fmt.Errorf("raftcmd: decode: %w", err)
	}
	return cmd, nil
}

// encodeApplyResult marshals an ApplyResult. Returned values
// from FSM.Apply are not actually serialised through the log —
// they ride back to the proposer in-process via the future. We
// still use protobuf for them to keep one tooling chain.
func encodeApplyResult(r *ApplyResult) []byte {
	if r == nil {
		return nil
	}
	b, err := proto.Marshal(r)
	if err != nil {
		// Apply results are tiny, fixed-shape; marshal can't fail
		// in practice. Returning nil lets callers see ErrApply.
		return nil
	}
	return b
}

// applyResultErr converts an ApplyResult into a Go error,
// preserving sentinel typing so callers can errors.Is against
// storage.ErrCASFailed etc.
func applyResultErr(r *ApplyResult) error {
	if r == nil || r.Error == "" {
		return nil
	}
	if r.CasFailed {
		return storage.ErrCASFailed
	}
	return errors.New(r.Error)
}

// newPutCmd / newDeleteCmd / newCASCmd / newCASCreateCmd /
// newBatchCmd are the constructor helpers used by RaftStorage
// when proposing writes. Origin + ProposedAt are populated here
// so the FSM never has to reach back to the storage handle.

func newPutCmd(origin, key string, value []byte) *Command {
	return &Command{
		Op:               Op_OP_PUT,
		Key:              key,
		Value:            value,
		Origin:           origin,
		ProposedAtUnixNs: time.Now().UnixNano(),
	}
}

func newDeleteCmd(origin, key string) *Command {
	return &Command{
		Op:               Op_OP_DELETE,
		Key:              key,
		Origin:           origin,
		ProposedAtUnixNs: time.Now().UnixNano(),
	}
}

func newCASCmd(origin, key string, expected, fresh []byte) *Command {
	return &Command{
		Op:               Op_OP_CAS,
		Key:              key,
		Value:            fresh,
		Expected:         expected,
		Origin:           origin,
		ProposedAtUnixNs: time.Now().UnixNano(),
	}
}

func newCASDeleteCmd(origin, key string, expected []byte) *Command {
	return &Command{
		Op:               Op_OP_CAS,
		Key:              key,
		Expected:         expected,
		DeleteOnApply:    true,
		Origin:           origin,
		ProposedAtUnixNs: time.Now().UnixNano(),
	}
}

func newCASCreateCmd(origin, key string, value []byte) *Command {
	return &Command{
		Op:               Op_OP_CAS_CREATE,
		Key:              key,
		Value:            value,
		ExpectedNil:      true,
		Origin:           origin,
		ProposedAtUnixNs: time.Now().UnixNano(),
	}
}

func newBatchCmd(origin string, ops []storage.BatchOp) *Command {
	entries := make([]*BatchEntry, 0, len(ops))
	for _, o := range ops {
		var bop Op
		switch o.Op {
		case storage.OpPut:
			bop = Op_OP_PUT
		case storage.OpDelete:
			bop = Op_OP_DELETE
		default:
			// Skip unknown ops; the FSM will validate again.
			continue
		}
		entries = append(entries, &BatchEntry{
			Op:    bop,
			Key:   o.Key,
			Value: o.Value,
		})
	}
	return &Command{
		Op:               Op_OP_BATCH,
		Batch:            entries,
		Origin:           origin,
		ProposedAtUnixNs: time.Now().UnixNano(),
	}
}
