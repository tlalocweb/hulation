package config

// Team — Raft-cluster identity and membership. Solo deployments
// can omit the entire `team:` block; hula auto-bootstraps a
// single-node cluster on first run and persists the generated
// IDs under DataDir.
//
// Multi-node config (Stage 3) sits in the same struct: peers
// are listed under `peers:` and the seed node sets
// `bootstrap: first-of-team`. Stage 2 parses peers but logs a
// warning that they are not yet honored.

// TeamConfig is the YAML surface for the Raft cluster the node
// participates in. See HA_PLAN2.md §4.
type TeamConfig struct {
	// TeamID is the cluster's UUID (typically v7). Auto-generated
	// on first boot when unset; the value is persisted under
	// DataDir/team-id so it survives restarts.
	TeamID string `yaml:"team_id,omitempty"`

	// NodeID is unique within the team. Default = OS hostname.
	NodeID string `yaml:"node_id,omitempty"`

	// DataDir is the root for raft state. Sub-paths created on
	// first boot:
	//   data.db          — FSM state
	//   raft-log.db      — raft log store
	//   raft-stable.db   — raft stable store
	//   snapshots/       — raft snapshots
	//   team-id          — persisted TeamID
	//   node-id          — persisted NodeID
	DataDir string `yaml:"data_dir,omitempty"`

	// Bootstrap controls cluster initialisation:
	//   "" / "solo"        — single-node bootstrap (default)
	//   "first-of-team"    — multi-node bootstrap (seed node, Stage 3)
	//   "join"             — join an existing cluster (Stage 3)
	//
	// Stage 2 only honors "" and "solo"; the others log a warn
	// and fall back to solo.
	Bootstrap string `yaml:"bootstrap,omitempty"`

	// BindAddr is the TCP address the raft transport binds to.
	// Solo defaults to 127.0.0.1:0 (loopback ephemeral). Stage 3
	// surfaces a routable address.
	BindAddr string `yaml:"bind_addr,omitempty"`

	// AdvertiseAddr is the address other peers see in raft
	// configuration. Solo leaves this empty. Stage 3 fills it in.
	AdvertiseAddr string `yaml:"advertise_addr,omitempty"`

	// Peers — Stage 3 surface. Stage 2 parses but ignores with
	// a warning.
	Peers []TeamPeer `yaml:"peers,omitempty"`

	// SnapshotInterval is the duration between automatic
	// snapshots. Default 5 minutes for solo, 2 minutes for
	// multi-node. Accepts Go duration strings ("30s", "10m").
	SnapshotInterval string `yaml:"snapshot_interval,omitempty"`

	// SnapshotThreshold is the log-entry count that triggers a
	// snapshot. Default 8192 entries.
	SnapshotThreshold uint64 `yaml:"snapshot_threshold,omitempty"`
}

// TeamPeer describes one node in the team. Stage 2 stores the
// data but does not wire it into raft.AddVoter — that lands in
// Stage 3.
type TeamPeer struct {
	ID       string `yaml:"id"`
	RaftAddr string `yaml:"raft_addr"`
	// InternalAddr is the gRPC + mTLS endpoint for the internal
	// API (analytics relay, chat fanout). Used in Stage 4.
	InternalAddr string `yaml:"internal_addr,omitempty"`
}

// DefaultTeamDataDir is the canonical filesystem root for raft
// state when the operator hasn't pinned one. Lives next to the
// other persistent data under /var/hula.
const DefaultTeamDataDir = "/var/hula/data/raft"
