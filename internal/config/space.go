package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Space is a free-space floor, written either as a share of the filesystem
// ("10%") or as an absolute amount ("50GiB").
//
// Both forms exist because neither works alone: a percentage is the right
// thing on a disk whose size you may change, while on a small disk 10% can be
// less than one night's growth, and only an absolute figure says "keep room
// for another backup".
type Space struct {
	raw     string
	Percent float64 // > 0 when the threshold is a share of the filesystem
	Bytes   uint64  // > 0 when the threshold is absolute
}

// suffixes are binary units; "G" is accepted as a short "GiB" because that is
// how such a value is usually typed.
var suffixes = []struct {
	name  string
	scale uint64
}{
	{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40}, {"PiB", 1 << 50},
	{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"T", 1 << 40}, {"P", 1 << 50},
	{"B", 1},
}

// ParseSpace reads one threshold. It is exported for tests and for anything
// that needs the same syntax outside YAML.
func ParseSpace(s string) (Space, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Space{}, fmt.Errorf("empty value; want a percentage like 10%% or a size like 50GiB")
	}

	if pct, ok := strings.CutSuffix(raw, "%"); ok {
		p, err := strconv.ParseFloat(strings.TrimSpace(pct), 64)
		if err != nil {
			return Space{}, fmt.Errorf("%q is not a percentage", raw)
		}
		if p <= 0 || p >= 100 {
			return Space{}, fmt.Errorf("%q must be above 0%% and below 100%%", raw)
		}
		return Space{raw: raw, Percent: p}, nil
	}

	upper := strings.ToUpper(raw)
	for _, suf := range suffixes {
		rest, ok := strings.CutSuffix(upper, strings.ToUpper(suf.name))
		if !ok {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil || n <= 0 {
			return Space{}, fmt.Errorf("%q is not a size", raw)
		}
		return Space{raw: raw, Bytes: uint64(n * float64(suf.scale))}, nil
	}

	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		return Space{}, fmt.Errorf("%q is neither a percentage like 10%% nor a size like 50GiB", raw)
	}
	return Space{raw: raw, Bytes: n}, nil
}

func (s *Space) UnmarshalYAML(unmarshal func(any) error) error {
	var str string
	if err := unmarshal(&str); err != nil {
		return err
	}
	parsed, err := ParseSpace(str)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

// MarshalYAML writes the threshold back exactly as it was configured, so
// `config dump` stays loadable.
func (s Space) MarshalYAML() (any, error) { return s.raw, nil }

func (s Space) String() string { return s.raw }

// Floor resolves the threshold against a filesystem's total size, so a
// percentage and an absolute amount can be compared the same way.
func (s Space) Floor(total uint64) uint64 {
	if s.Percent > 0 {
		return uint64(float64(total) * s.Percent / 100)
	}
	return s.Bytes
}
