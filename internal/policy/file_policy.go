// SPDX-License-Identifier: MIT

package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is the input to the file policy evaluator — describes the task.
// All fields are optional; missing fields don't satisfy any condition.
type Spec struct {
	File          string  `json:"file"`
	FileLines     int     `json:"file_lines"`
	FileCount     int     `json:"file_count"`
	FileExtension string  `json:"file_extension"`
	TaskType      string  `json:"task_type"`
	InPlaybook    bool    `json:"in_playbook"`
	StageName     string  `json:"stage_name"`
	HasGit        bool    `json:"has_git"`
	EnumTier      int     `json:"enum_tier"`
	Workspace     string  `json:"workspace"`
	Prompt        string  `json:"prompt"`
	PromptLength  int     `json:"prompt_length"`
	ContextPct    int     `json:"context_pct"`
}

// FilePolicy is the merged output of policy evaluation.
type FilePolicy struct {
	EditMode            string   `yaml:"edit_mode"`
	Atomic              bool     `yaml:"atomic"`
	UseWorktree         bool     `yaml:"use_worktree"`
	AutoCommit          bool     `yaml:"auto_commit"`
	TrackTokens         bool     `yaml:"track_tokens"`
	Validators          []string `yaml:"validators"`
	ValidateStrict      bool     `yaml:"validate_strict"`
	TestLoop            bool     `yaml:"test_loop"`
	LintLoop            bool     `yaml:"lint_loop"`
	MaxRetries          int      `yaml:"max_retries"`
	EscalateOnFail      bool     `yaml:"escalate_on_fail"`
	RubberDuck          bool     `yaml:"rubber_duck"`
	ConfidenceThreshold float64  `yaml:"confidence_threshold"`
	DiffSizeCapPct      int      `yaml:"diff_size_cap_pct"`
	MaxCostUSD          float64  `yaml:"max_cost_usd"`
	MaxWallSeconds      int      `yaml:"max_wall_seconds"`
	UseRepoMap          bool     `yaml:"use_repo_map"`
	DedupFileReads      bool     `yaml:"dedup_file_reads"`
	PromptCache         bool     `yaml:"prompt_cache"`
	Defensive           bool     `yaml:"defensive"`
	MatchedRules        []string `yaml:"-"`
}

type policyFile struct {
	Version  string                   `yaml:"version"`
	Defaults map[string]interface{}   `yaml:"defaults"`
	Rules    []policyRule             `yaml:"rules"`
}

type policyRule struct {
	Name  string                 `yaml:"name"`
	When  map[string]interface{} `yaml:"when"`
	Apply map[string]interface{} `yaml:"apply"`
}

// FilePolicyEngine evaluates registry/policy.yaml against a Spec.
type FilePolicyEngine struct {
	pf policyFile
}

// LoadFilePolicy reads registry/policy.yaml relative to hydraHome.
func LoadFilePolicy(hydraHome string) (*FilePolicyEngine, error) {
	path := filepath.Join(hydraHome, "registry", "policy.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy.yaml not found at %s", path)
	}
	var pf policyFile
	if err := yaml.Unmarshal(raw, &pf); err != nil {
		return nil, fmt.Errorf("parse policy.yaml: %w", err)
	}
	return &FilePolicyEngine{pf: pf}, nil
}

// Decide evaluates all rules against spec and returns the merged FilePolicy.
func (e *FilePolicyEngine) Decide(spec Spec) FilePolicy {
	// Start from defaults.
	fp := defaultFilePolicy()
	if e.pf.Defaults != nil {
		applyMap(e.pf.Defaults, &fp)
	}

	for _, rule := range e.pf.Rules {
		if matchWhen(rule.When, spec) {
			applyMap(rule.Apply, &fp)
			fp.MatchedRules = append(fp.MatchedRules, rule.Name)
		}
	}
	return fp
}

func defaultFilePolicy() FilePolicy {
	return FilePolicy{
		EditMode:       "rewrite",
		MaxRetries:     1,
		EscalateOnFail: true,
		TrackTokens:    true,
		DiffSizeCapPct: 90,
		MaxWallSeconds: 600,
	}
}

// matchWhen returns true if all conditions in the when block match spec.
func matchWhen(when map[string]interface{}, spec Spec) bool {
	if len(when) == 0 {
		return true
	}
	for key, val := range when {
		if key == "always" {
			continue
		}
		if !matchCondition(key, val, spec) {
			return false
		}
	}
	return true
}

// matchCondition evaluates a single (key, value) condition against spec.
func matchCondition(key string, val interface{}, spec Spec) bool {
	var op, field string
	for _, suffix := range []string{"_contains", "_present", "_lte", "_gte", "_lt", "_gt", "_ne", "_eq", "_in"} {
		if strings.HasSuffix(key, suffix) {
			op = strings.TrimPrefix(suffix, "_")
			field = strings.TrimSuffix(key, suffix)
			break
		}
	}
	if op == "" {
		op = "eq"
		field = key
	}

	specVal := specField(field, spec)

	switch op {
	case "eq":
		return fmt.Sprintf("%v", specVal) == fmt.Sprintf("%v", val)
	case "ne":
		return fmt.Sprintf("%v", specVal) != fmt.Sprintf("%v", val)
	case "gt", "lt", "gte", "lte":
		sn := toFloat(specVal)
		vn := toFloat(val)
		switch op {
		case "gt":  return sn > vn
		case "lt":  return sn < vn
		case "gte": return sn >= vn
		case "lte": return sn <= vn
		}
	case "in":
		sv := fmt.Sprintf("%v", specVal)
		if sv == "" {
			return false
		}
		switch list := val.(type) {
		case []interface{}:
			for _, item := range list {
				if fmt.Sprintf("%v", item) == sv {
					return true
				}
			}
		case []string:
			for _, item := range list {
				if item == sv {
					return true
				}
			}
		}
		return false
	case "contains":
		return strings.Contains(fmt.Sprintf("%v", specVal), fmt.Sprintf("%v", val))
	case "present":
		sv := fmt.Sprintf("%v", specVal)
		present := sv != "" && sv != "false" && sv != "0"
		wantPresent := fmt.Sprintf("%v", val) == "true"
		return present == wantPresent
	}
	return false
}

// specField returns the value of a spec field by name.
func specField(field string, spec Spec) interface{} {
	switch field {
	case "file":           return spec.File
	case "file_lines":     return spec.FileLines
	case "file_count":     return spec.FileCount
	case "file_extension": return spec.FileExtension
	case "task_type":      return spec.TaskType
	case "in_playbook":    return spec.InPlaybook
	case "stage_name":     return spec.StageName
	case "has_git":        return spec.HasGit
	case "enum_tier":      return spec.EnumTier
	case "workspace":      return spec.Workspace
	case "prompt":         return spec.Prompt
	case "prompt_length":  return spec.PromptLength
	case "context_pct":    return spec.ContextPct
	}
	return ""
}

// applyMap merges a yaml map into a FilePolicy using field name matching.
func applyMap(m map[string]interface{}, fp *FilePolicy) {
	v := reflect.ValueOf(fp).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		yamlTag := t.Field(i).Tag.Get("yaml")
		if yamlTag == "" || yamlTag == "-" {
			continue
		}
		val, ok := m[yamlTag]
		if !ok {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.String:
			fv.SetString(fmt.Sprintf("%v", val))
		case reflect.Bool:
			fv.SetBool(fmt.Sprintf("%v", val) == "true")
		case reflect.Int:
			fv.SetInt(int64(toFloat(val)))
		case reflect.Float64:
			fv.SetFloat(toFloat(val))
		case reflect.Slice:
			if list, ok := val.([]interface{}); ok {
				ss := make([]string, len(list))
				for i, v := range list {
					ss[i] = fmt.Sprintf("%v", v)
				}
				fv.Set(reflect.ValueOf(ss))
			}
		}
	}
}

func toFloat(v interface{}) float64 {
	switch n := v.(type) {
	case int:     return float64(n)
	case int64:   return float64(n)
	case float64: return n
	case float32: return float64(n)
	case bool:
		if n { return 1 }
		return 0
	case string:
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "policy: non-numeric value %q treated as 0\n", n)
		}
		return f
	}
	return 0
}
