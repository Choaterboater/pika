package workrec

import (
	"encoding/json"
	"time"

	"github.com/Choaterboater/pika/internal/verify"
)

// Lifecycle phases. A record's Phase names the last phase that completed
// durably, which is exactly what `pika resume` restarts from.
const (
	PhaseBaseline = "baseline"
	PhaseHandoff  = "handoff"
	PhaseRecheck  = "recheck"
	PhaseDeliver  = "deliver"
)

// Terminal outcomes. A record with an empty Outcome is still in flight.
const (
	OutcomeComplete  = "complete"
	OutcomeBlocked   = "blocked"
	OutcomeAbandoned = "abandoned"
)

// Kinds of work a run carries.
const (
	KindRepair  = "repair"
	KindFeature = "feature"
)

// PhaseStamp records that a phase completed, and when. The slice of
// stamps on a Record is the run's history; Phase is its head.
type PhaseStamp struct {
	Phase string    `json:"phase"`
	At    time.Time `json:"at"`
	Note  string    `json:"note,omitempty"`
}

// Record is one run's durable state: everything `pika resume` needs to
// rejoin a run it did not start. It is a flat document on purpose — the
// whole record is rewritten atomically on every phase transition, so
// there is no partial-update path to get wrong.
type Record struct {
	WorkID     string `json:"work_id"`
	Goal       string `json:"goal,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Phase      string `json:"phase,omitempty"`
	Branch     string `json:"branch,omitempty"`
	BaseCommit string `json:"base_commit,omitempty"`
	Commit     string `json:"commit,omitempty"`

	Baseline *verify.Report `json:"baseline,omitempty"`
	Recheck  *verify.Report `json:"recheck,omitempty"`

	Role    string `json:"role,omitempty"`
	Runtime string `json:"runtime,omitempty"`

	Outcome string `json:"outcome,omitempty"`
	Reason  string `json:"reason,omitempty"`

	Phases []PhaseStamp `json:"phases,omitempty"`
}

// encode renders the record as indented JSON with a trailing newline, so
// a record read by a human or by `git diff` looks like every other pika
// document.
func encode(rec Record) ([]byte, error) {
	bs, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(bs, '\n'), nil
}
