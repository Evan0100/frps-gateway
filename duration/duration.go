// Package duration parses and formats whitelist TTLs with day support,
// e.g. "30m", "2h", "3d", "1d12h".
package duration

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Parse accepts one or more <number><unit> segments where unit is d/h/m/s.
// Combinations like "1d12h" are allowed. Negative or empty input is rejected.
func Parse(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	var total time.Duration
	rest := s
	for rest != "" {
		i := 0
		for i < len(rest) && (rest[i] >= '0' && rest[i] <= '9' || rest[i] == '.') {
			i++
		}
		if i == 0 {
			return 0, fmt.Errorf("invalid duration %q: expected a number", s)
		}
		numStr := rest[:i]
		num, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: bad number %q", s, numStr)
		}
		if i >= len(rest) {
			return 0, fmt.Errorf("invalid duration %q: missing unit after %q", s, numStr)
		}
		var unit time.Duration
		switch rest[i] {
		case 'd':
			unit = 24 * time.Hour
		case 'h':
			unit = time.Hour
		case 'm':
			unit = time.Minute
		case 's':
			unit = time.Second
		default:
			return 0, fmt.Errorf("invalid duration %q: unknown unit %q", s, string(rest[i]))
		}
		total += time.Duration(num * float64(unit))
		rest = rest[i+1:]
	}
	return total, nil
}

// Format renders a duration compactly using at most the two most significant
// non-zero units: "3d2h", "5h3m", "2m30s", "45s", "2h".
func Format(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	type segment struct {
		unit string
		size time.Duration
	}
	segments := []segment{
		{"d", 24 * time.Hour},
		{"h", time.Hour},
		{"m", time.Minute},
		{"s", time.Second},
	}
	var out []string
	for _, seg := range segments {
		if v := d / seg.size; v > 0 {
			out = append(out, strconv.FormatInt(int64(v), 10)+seg.unit)
			d -= v * seg.size
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "0s"
	}
	return strings.Join(out, "")
}
