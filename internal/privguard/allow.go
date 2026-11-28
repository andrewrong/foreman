package privguard

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// ParseAllowReasons reads .privguard/allow.yaml: a list of registered reason
// ids for the inline "privguard:allow <id>" marker.
//
//	reasons:
//	  - id: rule-definition
//	    why: the line defines a rule, it is not a leak
func ParseAllowReasons(data []byte) ([]string, error) {
	var f struct {
		Reasons []struct {
			ID  string `yaml:"id"`
			Why string `yaml:"why"`
		} `yaml:"reasons"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("privguard: allow.yaml: %w", err)
	}
	var ids []string
	for _, r := range f.Reasons {
		if r.ID == "" || r.Why == "" {
			return nil, fmt.Errorf("privguard: allow.yaml: every reason needs id and why")
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}
