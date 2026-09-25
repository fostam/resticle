package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads and strictly decodes a configuration file. Unknown fields are
// errors: a typo in a key must not silently disable a setting.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for name, j := range cfg.Jobs {
		if j == nil {
			return nil, fmt.Errorf("job %q is empty", name)
		}
		j.Name = name
	}
	cfg.JobOrder = jobOrder(path)
	return &cfg, nil
}

// jobOrder re-reads the file to recover the order of the `jobs:` mapping
// keys, which yaml.v3 does not preserve when decoding into a Go map.
func jobOrder(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var raw struct {
		Jobs yaml.Node `yaml:"jobs"`
	}
	if err := yaml.NewDecoder(f).Decode(&raw); err != nil {
		return nil
	}
	if raw.Jobs.Kind != yaml.MappingNode {
		return nil
	}
	order := make([]string, 0, len(raw.Jobs.Content)/2)
	for i := 0; i+1 < len(raw.Jobs.Content); i += 2 {
		order = append(order, raw.Jobs.Content[i].Value)
	}
	return order
}
