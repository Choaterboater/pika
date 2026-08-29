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

	"github.com/Choaterboater/projectctl/internal/contract"
	"github.com/Choaterboater/projectctl/internal/yamlx"
)

// ExceptionsFile is the exceptions record's location relative to the
// repository root (spec §5.3: .project/exceptions.yaml).
const ExceptionsFile = ".project/exceptions.yaml"

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

// LoadExceptions reads and strictly parses ExceptionsFile under repoRoot.
// A missing (or empty) file records no exceptions and is not an error. An
// existing file that fails strict parsing, or that carries an invalid
// record — an unknown field, a duplicate key, an escaping path, or a
// missing rule-id/reason/owner/review-condition — is a load error, never a
// silent skip: unverifiable exceptions must not silently widen the rules.
func LoadExceptions(repoRoot string) (map[string]Exception, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(ExceptionsFile)))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]Exception{}, nil
		}
		return nil, fmt.Errorf("%s: %w", ExceptionsFile, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]Exception{}, nil
	}

	var raw map[string]Exception
	if err := yamlx.UnmarshalStrict(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", ExceptionsFile, err)
	}

	out := make(map[string]Exception, len(raw))
	// Validate in key order so load errors are deterministic.
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ex := raw[key]
		norm, err := contract.NormalizeRepoPath(key)
		if err != nil {
			return nil, fmt.Errorf("%s: exception path %q: %w", ExceptionsFile, key, err)
		}
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
		ex.Path = norm
		out[norm] = ex
	}
	return out, nil
}
