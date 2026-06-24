package core

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// Sample is one data point from a Prometheus text-format metric.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// MetricFamily maps metric names to their samples.
type MetricFamily map[string][]Sample

// ScrapeMetrics fetches a Prometheus /metrics endpoint and returns all samples.
// Samples with NaN or infinite values are dropped.
func ScrapeMetrics(ctx context.Context, client *http.Client, url string) (MetricFamily, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return parsePrometheusText(io.LimitReader(resp.Body, 32<<20)) // 32 MB cap
}

func parsePrometheusText(r io.Reader) (MetricFamily, error) {
	fam := make(MetricFamily)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		name, labels, value, ok := parseSampleLine(line)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		fam[name] = append(fam[name], Sample{Labels: labels, Value: value})
	}
	return fam, scanner.Err()
}

// parseSampleLine parses one Prometheus exposition line: name{labels} value [timestamp]
func parseSampleLine(line string) (name string, labels map[string]string, value float64, ok bool) {
	// Try to strip optional trailing timestamp: "name{} value ts"
	// Strategy: find the last space; if what follows is a float, that's the value.
	// If not (it's a timestamp), look for the second-to-last space for the value.
	last := strings.LastIndexByte(line, ' ')
	if last < 0 {
		return
	}
	rightmost := line[last+1:]
	rest := line[:last]

	v, err := strconv.ParseFloat(rightmost, 64)
	if err != nil {
		// rightmost is a timestamp; value is the token before it
		prev := strings.LastIndexByte(rest, ' ')
		if prev < 0 {
			return
		}
		v, err = strconv.ParseFloat(rest[prev+1:], 64)
		if err != nil {
			return
		}
		rest = rest[:prev]
	}
	value = v

	if idx := strings.IndexByte(rest, '{'); idx >= 0 {
		name = rest[:idx]
		closing := strings.LastIndexByte(rest, '}')
		if closing < 0 {
			closing = len(rest)
		}
		labels = parseLabels(rest[idx+1 : closing])
	} else {
		name = rest
		labels = map[string]string{}
	}
	ok = true
	return
}

func parseLabels(s string) map[string]string {
	labels := map[string]string{}
	for s != "" {
		s = strings.TrimSpace(s)
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		key := strings.TrimSpace(s[:eq])
		s = s[eq+1:]
		if !strings.HasPrefix(s, `"`) {
			break
		}
		s = s[1:]
		var val strings.Builder
		for len(s) > 0 {
			if s[0] == '"' {
				s = s[1:]
				break
			}
			if s[0] == '\\' && len(s) > 1 {
				switch s[1] {
				case 'n':
					val.WriteByte('\n')
				case '\\':
					val.WriteByte('\\')
				case '"':
					val.WriteByte('"')
				default:
					val.WriteByte(s[1])
				}
				s = s[2:]
			} else {
				val.WriteByte(s[0])
				s = s[1:]
			}
		}
		labels[key] = val.String()
		s = strings.TrimPrefix(s, ",")
	}
	return labels
}

// --- Convenience accessors on MetricFamily ---

// First returns the first sample value; def if the metric has no samples.
func (f MetricFamily) First(name string, def float64) float64 {
	if s := f[name]; len(s) > 0 {
		return s[0].Value
	}
	return def
}

// FirstAlt returns the first sample of name; falls back to alt, then def.
// Useful when a metric was renamed between exporter versions.
func (f MetricFamily) FirstAlt(name, alt string, def float64) float64 {
	if s := f[name]; len(s) > 0 {
		return s[0].Value
	}
	if s := f[alt]; len(s) > 0 {
		return s[0].Value
	}
	return def
}

// FirstWhere returns the first sample value where all matchLabels are present; def if none found.
func (f MetricFamily) FirstWhere(name string, match map[string]string, def float64) float64 {
	for _, s := range f[name] {
		if labelsContain(s.Labels, match) {
			return s.Value
		}
	}
	return def
}

// Where returns all samples for name whose labels contain all pairs in match.
func (f MetricFamily) Where(name string, match map[string]string) []Sample {
	var out []Sample
	for _, s := range f[name] {
		if labelsContain(s.Labels, match) {
			out = append(out, s)
		}
	}
	return out
}

// Sum returns the sum of all sample values for name.
func (f MetricFamily) Sum(name string) float64 {
	var sum float64
	for _, s := range f[name] {
		sum += s.Value
	}
	return sum
}

// SumWhere sums sample values for name where labelKey == labelValue.
func (f MetricFamily) SumWhere(name, labelKey, labelValue string) float64 {
	var sum float64
	for _, s := range f[name] {
		if s.Labels[labelKey] == labelValue {
			sum += s.Value
		}
	}
	return sum
}

// SumAlt sums name; if name has no samples, tries alt.
func (f MetricFamily) SumAlt(name, alt string) float64 {
	if v := f.Sum(name); v != 0 || len(f[name]) > 0 {
		return v
	}
	return f.Sum(alt)
}

// LabelValues returns the distinct values of labelKey across all samples of name.
func (f MetricFamily) LabelValues(name, labelKey string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range f[name] {
		v := s.Labels[labelKey]
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func labelsContain(labels, match map[string]string) bool {
	for k, v := range match {
		if labels[k] != v {
			return false
		}
	}
	return true
}
