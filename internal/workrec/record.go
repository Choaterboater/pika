package workrec

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/Choaterboater/pika/internal/redact"
	"github.com/Choaterboater/pika/internal/verify"
)

// Lifecycle phases. A record's Phase names the last phase that completed
// durably, which is exactly what `pika resume` restarts from.
//
// Explore and review are optional: a contract that configures neither
// stamps neither, and a default contract still produces exactly
// baseline, handoff, recheck, deliver.
const (
	PhaseBaseline = "baseline"
	PhaseExplore  = "explore"
	PhaseHandoff  = "handoff"
	PhaseRecheck  = "recheck"
	PhaseReview   = "review"
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

// RunAgent records one agent a run actually spawned, in spawn order.
//
// Role, Agent and Runtime are all kernel-generated identity — the role the
// contract assigned, the contract key it resolved from, and the runtime
// that ran — so redacted() leaves all three alone, exactly as it does the
// singular Role and Runtime this generalizes. Calls, TokensIn and TokensOut
// are the same kind of fact: kernel-generated counters of what the run
// spent, reported only by runtimes that can know them (a subprocess runner
// would be guessing), so they are left alone too. All three are omitempty,
// so a record whose runtime does not report — and every pre-M7 record —
// encodes byte-identical to before.
type RunAgent struct {
	Role      string `json:"role"`
	Agent     string `json:"agent"`
	Runtime   string `json:"runtime"`
	Calls     int    `json:"calls,omitempty"`
	TokensIn  int    `json:"tokens_in,omitempty"`
	TokensOut int    `json:"tokens_out,omitempty"`
}

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
	WorkID     string         `json:"work_id"`
	Goal       string         `json:"goal,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Phase      string         `json:"phase,omitempty"`
	Branch     string         `json:"branch,omitempty"`
	BaseCommit string         `json:"base_commit,omitempty"`
	Baseline   *verify.Report `json:"baseline,omitempty"`
	Recheck    *verify.Report `json:"recheck,omitempty"`

	Role    string `json:"role,omitempty"`
	Runtime string `json:"runtime,omitempty"`

	// Agents is every agent this run spawned, in spawn order. Role and
	// Runtime above stay because a record written before M6 carries them
	// and `pika resume` has to rejoin one without reading a field that
	// did not exist when it was written.
	Agents []RunAgent `json:"agents,omitempty"`

	Commit string `json:"commit,omitempty"`

	Outcome string `json:"outcome,omitempty"`
	Reason  string `json:"reason,omitempty"`

	Phases []PhaseStamp `json:"phases,omitempty"`
}

// encode renders the record as indented JSON with a trailing newline, so
// a record read by a human or by `git diff` looks like every other pika
// document.
//
// HTML escaping is off. json.Marshal would otherwise write the redaction
// placeholders this package now produces as "\u003credacted:oauth\u003e",
// which parses the same but is unreadable in exactly the situation the
// record exists for — a human catting record.json to see why a run is
// stuck. Nothing here is ever interpolated into HTML.
func encode(rec Record) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	// Encode already terminates the document with a newline.
	if err := enc.Encode(rec); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// redacted returns rec with every free-text and captured-output string
// run through redact.Apply — the same treatment evidence.Build gives
// every string it emits. Save applies it before encoding, so no caller
// can write an unredacted record by forgetting to ask.
//
// This is defence in depth, deliberately. record.json lives under
// .project/state/, which is gitignored and filtered out of handoff
// bundles and history — but that guarantee rests on one prefix test
// against a path, and that test has already been wrong twice: once for
// staged renames, once for quoted paths. Redacting here means the next
// filter bug leaks placeholders instead of credentials. Do not remove
// this on the grounds that the filter already covers it; the filter is
// exactly what this is insurance against.
//
// What is NOT redacted is as deliberate: WorkID, Kind, Phase, Branch,
// the commits, Role, Runtime and Outcome are the record's identity —
// the structural fields `pika resume` uses to rejoin the world a run
// belongs to, all kernel-generated or validated (the work id by
// evidence.ValidateWorkID, which is also what names the directory this
// file sits in). Rewriting one of them would not protect anything and
// would break the resume, and a record.json whose work_id disagrees
// with its directory is a record Open refuses outright.
func redacted(rec Record) Record {
	rec.Goal = redact.Apply(rec.Goal)
	rec.Reason = redact.Apply(rec.Reason)
	if len(rec.Phases) > 0 {
		phases := make([]PhaseStamp, len(rec.Phases))
		for i, p := range rec.Phases {
			p.Note = redact.Apply(p.Note)
			phases[i] = p
		}
		rec.Phases = phases
	}
	rec.Baseline = redactReport(rec.Baseline)
	rec.Recheck = redactReport(rec.Recheck)
	return rec
}

// redactReport returns a redacted copy of a verify report. It copies
// rather than rewriting in place because the caller still holds the
// report it just rendered to the terminal: a save must not reach back
// and edit the live one.
//
// Every string a report carries is either captured process output or
// derived from it — OutputTail is the raw last 8 KiB of a gate's
// stdout and stderr, Failure.Detail embeds that tail, and Cmd is the
// argv of a command discovered in the repository, which is where a
// token pasted into a check command would sit. Gate ids are the
// contract's own slot names and stay legible.
func redactReport(rep *verify.Report) *verify.Report {
	if rep == nil {
		return nil
	}
	out := *rep
	out.Gates = make([]verify.GateResult, len(rep.Gates))
	for i, g := range rep.Gates {
		if len(g.Cmd) > 0 {
			cmd := make([]string, len(g.Cmd))
			for j, arg := range g.Cmd {
				cmd[j] = redact.Apply(arg)
			}
			g.Cmd = cmd
		}
		g.OutputTail = redact.Apply(g.OutputTail)
		g.Reason = redact.Apply(g.Reason)
		out.Gates[i] = g
	}
	out.Baseline = redactFailures(rep.Baseline)
	out.Regressions = redactFailures(rep.Regressions)
	if len(rep.Warnings) > 0 {
		out.Warnings = make([]string, len(rep.Warnings))
		for i, w := range rep.Warnings {
			out.Warnings[i] = redact.Apply(w)
		}
	}
	return &out
}

func redactFailures(fs []verify.Failure) []verify.Failure {
	if len(fs) == 0 {
		return nil
	}
	out := make([]verify.Failure, len(fs))
	for i, f := range fs {
		f.Detail = redact.Apply(f.Detail)
		out[i] = f
	}
	return out
}
