package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// The multi-role lifecycle: one run, two runtimes.
//
// Six of the seven runtimes the contract schema accepts were refused by
// the binary before M6, and the role set in design §9.1 was unreachable
// for the same reason — pika had no vocabulary for spawning a second
// agent under a different runtime. This step drives both through the real
// binary against the same fixture the end-to-end suite uses: no model, no
// credential, no network.
//
// It is one step rather than two because what it proves is a conjunction:
// that the run completed, and that it did so with two agents under two
// runtimes. Either alone is a claim M6 did not make.

// rolesAgent names a claude builder and an omp reviewer. Neither runtime
// is codex, which is the point: before M6 the first was refused by name
// and the second did not exist.
const rolesAgent = "agents:\n  builder:\n    runtime: claude\n  reviewer:\n    runtime: omp\n"

// rolesGoal is the goal the run is given. `pika work` takes a goal
// whether or not the ladder is green, which is what makes it the command
// for a step about the roles rather than about a repair.
const rolesGoal = "add a NOTES.md the ladder can verify"

// rolesEntry is the edit the fake agent makes: a well-formed Go file the
// scaffold's own gates stay green through.
const (
	rolesPath    = "NOTES.md"
	rolesContent = "# Notes\n\nWritten by the run's builder.\n"
)

// runRecord is the durable record `pika work` leaves under
// .project/state/work/<work-id>/record.json, in the fields this step
// reads. It is deliberately partial: a smoke gate asserts the facts it
// names and nothing else, so a new field cannot break it.
type runRecord struct {
	WorkID  string `json:"work_id"`
	Runtime string `json:"runtime"`
	Agents  []struct {
		Role    string `json:"role"`
		Agent   string `json:"agent"`
		Runtime string `json:"runtime"`
	} `json:"agents"`
	Phases []struct {
		Phase string `json:"phase"`
	} `json:"phases"`
}

func stepRoles(h *harness) error {
	c := &check{}
	dir, _, err := h.scaffold("roles")
	if err != nil {
		return err
	}
	doc, err := readRepo(dir, ".project/contract.yaml")
	if err != nil {
		return err
	}
	if !strings.Contains(doc, scaffoldedAgents) {
		c.failf("`pika init` no longer scaffolds %q, so this step cannot name its agents:\n%s",
			scaffoldedAgents, doc)
		return c.err()
	}
	if err := writeRepo(dir, ".project/contract.yaml", strings.Replace(doc, scaffoldedAgents, rolesAgent, 1)); err != nil {
		return err
	}
	if _, err := initGit(dir); err != nil {
		return err
	}

	argvPath := filepath.Join(h.dir, "roles-argv.txt")
	r, err := h.run(dir, h.agentEnv(
		"FAKE_AGENT_FILE="+rolesPath,
		"FAKE_AGENT_CONTENT="+rolesContent,
		"FAKE_AGENT_ARGV="+argvPath,
		// The reviewer is read-only and the run refuses it if it is
		// not, so the edit belongs to the builder alone. One fixture
		// plays both roles, and the flag only the claude adapter sends
		// is what tells them apart.
		"FAKE_AGENT_EDIT_ON=--permission-mode",
	), "work", rolesGoal, "--json")
	if err != nil {
		return err
	}
	wantEqual(c, "`pika work` exit code on a two-runtime run", r.exit, 0)

	var workID string
	{
		var payload struct {
			Result struct {
				WorkID string `json:"workId"`
				Commit string `json:"commit"`
			} `json:"result"`
			OK bool `json:"ok"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &payload); err != nil {
			c.failf("`pika work --json` printed no envelope\n%s", r)
			return c.err()
		}
		c.truef(payload.OK, "`pika work` reported not ok on a two-runtime run\n%s", r)
		c.truef(payload.Result.Commit != "", "`pika work` delivered no commit, so the review gated it\n%s", r)
		workID = payload.Result.WorkID
	}
	if workID == "" {
		c.failf("`pika work` reported no work id, so there is no record to read\n%s", r)
		return c.err()
	}

	// The record is what the run says about itself afterwards. Two
	// agents, in spawn order, under two runtimes — and a review phase
	// the pre-M6 lifecycle never stamped.
	raw, err := os.ReadFile(filepath.Join(dir, ".project", "state", "work", workID, "record.json"))
	if err != nil {
		c.failf("the run left no durable record: %v\n%s", err, r)
		return c.err()
	}
	var rec runRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		c.failf("the run's record is not JSON: %v\n%s", err, quoteBlock("record", string(raw)))
		return c.err()
	}
	var runtimes []string
	for _, a := range rec.Agents {
		runtimes = append(runtimes, a.Role+"="+a.Runtime)
	}
	c.contains("the runtimes the run's record names", strings.Join(runtimes, " "), "builder=claude", "reviewer=omp")
	c.truef(len(rec.Agents) == 2, "the run recorded %d agents, want 2: %s", len(rec.Agents), strings.Join(runtimes, " "))

	var phaseNames []string
	for _, p := range rec.Phases {
		phaseNames = append(phaseNames, p.Phase)
	}
	c.contains("the phases the run stamped", strings.Join(phaseNames, " "), "review")
	// The review ran after the recheck it read, and the run still
	// delivered: an advisory review that gated a green ladder would be a
	// second gate that is not deterministic.
	c.contains("the phases the run stamped", strings.Join(phaseNames, " "), "recheck")
	return c.err()
}
