package checks

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	goyaml "github.com/goccy/go-yaml"

	"github.com/Choaterboater/pika/internal/contract"
	"github.com/Choaterboater/pika/internal/yamlx"
)

// ExceptionsFile is the exceptions record's location relative to the
// repository root (spec §5.3: .project/exceptions.yaml).
const ExceptionsFile = ".project/exceptions.yaml"

// AutoRecordedOwner is the placeholder `pika adopt` writes into every
// exception it inherits unattended (naming-catch-all and
// naming-kebab-case records adopt cannot ask a human about at adoption
// time). It is honest — nobody has accepted the record yet — but
// nothing forces it to be reassigned, so a repository that never
// revisits its exceptions accumulates durable waivers with no human
// behind them. Gate 1 warns when it sees this owner; it does not fail,
// because unreviewed is not invalid.
const AutoRecordedOwner = "pika adopt"

// Exception is one recorded exception to a naming rule (spec §5.3: an
// exception requires a rule ID, a rationale, an owner, and a review
// condition — all four must be non-empty). Path is the excepted
// repository path; it comes from the record's mapping key.
type Exception struct {
	RuleID          string `yaml:"rule-id"          json:"ruleId"`
	Path            string `yaml:"path"             json:"path"`
	Reason          string `yaml:"reason"           json:"reason"`
	Owner           string `yaml:"owner"            json:"owner"`
	ReviewCondition string `yaml:"review-condition" json:"reviewCondition"`
}

// exceptionList is one path's recorded exceptions. A path violating two
// different naming rules at once — a banned catch-all directory
// segment and a non-kebab-case filename segment in the same path is an
// ordinary shape, not a rare one — needs one exception per rule, and
// exceptions.yaml's map is keyed by path alone. Rather than change the
// key, each path's value may be either a single exception object (the
// common case, and the only shape every exceptions.yaml written before
// this existed) or a list of them.
//
// The custom decode makes this shape parse; internal/yamlx's checkKeys
// separately makes strictness see through it, matching a bare object
// against Exception's fields instead of silently passing it through
// because the node is not a sequence.
type exceptionList []Exception

func (l *exceptionList) UnmarshalYAML(b []byte) error {
	var list []Exception
	if err := goyaml.Unmarshal(b, &list); err == nil {
		*l = list
		return nil
	}
	var single Exception
	if err := goyaml.Unmarshal(b, &single); err != nil {
		return err
	}
	*l = []Exception{single}
	return nil
}

// LoadExceptions reads and strictly parses ExceptionsFile under repoRoot.
// A missing (or empty) file records no exceptions and is not an error. An
// existing file that fails strict parsing, or that carries an invalid
// record — an unknown field, a duplicate key, an escaping path, a
// missing rule-id/reason/owner/review-condition, or two exceptions for
// the same path recording the same rule twice — is a load error, never
// a silent skip: unverifiable exceptions must not silently widen the
// rules.
//
// A path may carry more than one exception, one per rule it excepts;
// excepted() (naming.go) checks a path's whole list for the rule it is
// asking about, not just the first entry.
func LoadExceptions(repoRoot string) (map[string][]Exception, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(ExceptionsFile)))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string][]Exception{}, nil
		}
		return nil, fmt.Errorf("%s: %w", ExceptionsFile, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string][]Exception{}, nil
	}

	var raw map[string]exceptionList
	if err := yamlx.UnmarshalStrict(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", ExceptionsFile, err)
	}

	out := make(map[string][]Exception, len(raw))
	// Validate in key order so load errors are deterministic.
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		norm, err := contract.NormalizeRepoPath(key)
		if err != nil {
			return nil, fmt.Errorf("%s: exception path %q: %w", ExceptionsFile, key, err)
		}
		seenRules := map[string]bool{}
		list := make([]Exception, 0, len(raw[key]))
		for _, ex := range raw[key] {
			if ex.Path != "" && ex.Path != key && ex.Path != norm {
				return nil, fmt.Errorf("%s: exception %q: path field %q disagrees with the mapping key", ExceptionsFile, key, ex.Path)
			}
			var missing []string
			for field, val := range map[string]string{
				"rule-id":          ex.RuleID,
				"reason":           ex.Reason,
				"owner":            ex.Owner,
				"review-condition": ex.ReviewCondition,
			} {
				if strings.TrimSpace(val) == "" {
					missing = append(missing, field)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				return nil, fmt.Errorf("%s: exception %q: missing %s", ExceptionsFile, key, strings.Join(missing, ", "))
			}
			if seenRules[ex.RuleID] {
				return nil, fmt.Errorf("%s: exception %q: rule %q is recorded twice", ExceptionsFile, key, ex.RuleID)
			}
			seenRules[ex.RuleID] = true
			ex.Path = norm
			list = append(list, ex)
		}
		out[norm] = list
	}
	return out, nil
}
