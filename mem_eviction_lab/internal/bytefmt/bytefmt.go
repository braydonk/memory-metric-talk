package bytefmt

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Limit represents a cgroup memory limit value ("max" or an exact byte count).
type Limit struct {
	Unlimited bool
	Bytes     uint64
}

// String formats the limit for writing directly to cgroup v2 files (e.g. memory.max or memory.swap.max).
func (l Limit) String() string {
	if l.Unlimited {
		return "max"
	}
	return strconv.FormatUint(l.Bytes, 10)
}

// Human formats the limit for display (e.g. "Unlimited (max)", "0 B", "60.0 MiB").
func (l Limit) Human() string {
	if l.Unlimited {
		return "Unlimited (max)"
	}
	return FormatIEC(l.Bytes)
}

// ParseLimit parses a cgroup limit string such as "max", "0" (if allowZero is true),
// exact integer bytes ("62914560"), SI suffixes ("80MB", "80KB", "1GB"), or
// IEC suffixes ("60MiB", "60M", "512KiB", "1GiB").
func ParseLimit(raw string, allowZero bool) (Limit, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Limit{}, fmt.Errorf("empty byte limit")
	}
	if strings.EqualFold(s, "max") {
		return Limit{Unlimited: true}, nil
	}

	// Split numeric prefix from optional unit suffix.
	numEnd := 0
	for numEnd < len(s) {
		c := s[numEnd]
		if (c >= '0' && c <= '9') || c == '.' {
			numEnd++
			continue
		}
		break
	}
	if numEnd == 0 {
		return Limit{}, fmt.Errorf("invalid byte limit %q: missing numeric value", raw)
	}

	numPart := s[:numEnd]
	unitPart := strings.ToLower(strings.TrimSpace(s[numEnd:]))

	multiplier, err := unitMultiplier(unitPart)
	if err != nil {
		return Limit{}, fmt.Errorf("invalid byte limit %q: %w", raw, err)
	}

	var bytesVal uint64
	if !strings.Contains(numPart, ".") {
		u, err := strconv.ParseUint(numPart, 10, 64)
		if err != nil {
			return Limit{}, fmt.Errorf("invalid byte limit %q: %w", raw, err)
		}
		if u > 0 && multiplier > math.MaxUint64/u {
			return Limit{}, fmt.Errorf("byte limit %q overflows uint64", raw)
		}
		bytesVal = u * multiplier
	} else {
		f, err := strconv.ParseFloat(numPart, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
			return Limit{}, fmt.Errorf("invalid byte limit %q", raw)
		}
		prod := f * float64(multiplier)
		if prod > float64(math.MaxUint64) {
			return Limit{}, fmt.Errorf("byte limit %q overflows uint64", raw)
		}
		bytesVal = uint64(math.Round(prod))
	}

	if bytesVal == 0 && !allowZero {
		return Limit{}, fmt.Errorf("byte limit %q must be greater than 0 (or \"max\")", raw)
	}
	return Limit{Bytes: bytesVal}, nil
}

func unitMultiplier(unit string) (uint64, error) {
	switch unit {
	case "", "b":
		return 1, nil
	case "k", "ki", "kib":
		return 1024, nil
	case "kb":
		return 1000, nil
	case "m", "mi", "mib":
		return 1024 * 1024, nil
	case "mb":
		return 1000 * 1000, nil
	case "g", "gi", "gib":
		return 1024 * 1024 * 1024, nil
	case "gb":
		return 1000 * 1000 * 1000, nil
	case "t", "ti", "tib":
		return 1024 * 1024 * 1024 * 1024, nil
	case "tb":
		return 1000 * 1000 * 1000 * 1000, nil
	default:
		return 0, fmt.Errorf("unknown unit %q", unit)
	}
}

// FormatIEC formats bytes in human-readable IEC units (KiB, MiB, GiB).
func FormatIEC(b uint64) string {
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.2f GiB", float64(b)/float64(gib))
	case b >= mib:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(mib))
	case b >= kib:
		return fmt.Sprintf("%.1f KiB", float64(b)/float64(kib))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
