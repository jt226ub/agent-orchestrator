package agy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const (
	// capacityTerminationWait mirrors the model catalog's grace period after
	// context cancellation before the CLI is killed.
	capacityTerminationWait = 2 * time.Second
	capacityOutputLimit     = 1 << 20
)

var _ ports.AgyCapacityReader = (*Plugin)(nil)

// capacityEnvKeysUnset are removed from the CLI's environment for the read: a
// configured Gemini API key makes the CLI bill the key instead of the plan,
// and the plan is what this reader measures.
var capacityEnvKeysUnset = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}

// ReadAgyCapacity runs the CLI's own `/usage` command headless and returns the
// plan limits it reports. Confirmed via `agy -p "/usage" --output-format json`:
// the envelope carries `command.data.groups[].buckets[]` with a 5-hour and a
// weekly window per model group.
func (p *Plugin) ReadAgyCapacity(ctx context.Context) (ports.AgyCapacityObservation, error) {
	binary, err := p.agyBinary(ctx)
	if err != nil {
		return ports.AgyCapacityObservation{}, err
	}
	return runAgyUsage(ctx, binary, environmentWithout(os.Environ(), capacityEnvKeysUnset))
}

// runAgyPrint runs one headless CLI command with the given environment and
// returns bounded stdout and stderr. The CLI's error text is classified by
// callers and never retained beyond that.
func runAgyPrint(ctx context.Context, binary string, env []string, prompt string) (string, string, error) {
	cmd := aoprocess.CommandContext(ctx, binary, "-p", prompt, "--output-format", "json") //nolint:gosec // binary is adapter-resolved, args are static
	cmd.WaitDelay = capacityTerminationWait
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &stdout, limit: capacityOutputLimit}
	cmd.Stderr = &limitedWriter{buf: &stderr, limit: capacityOutputLimit}
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

type agyUsageEnvelope struct {
	Status  string `json:"status"`
	Error   string `json:"error"`
	Command *struct {
		Name string `json:"name"`
		Data struct {
			Groups []agyUsageGroup `json:"groups"`
		} `json:"data"`
	} `json:"command"`
}

type agyUsageGroup struct {
	Name    string           `json:"name"`
	Buckets []agyUsageBucket `json:"buckets"`
}

type agyUsageBucket struct {
	ID                string  `json:"id"`
	Window            string  `json:"window"`
	RemainingFraction float64 `json:"remaining_fraction"`
	ResetTime         string  `json:"reset_time"`
}

// parseAgyUsage maps the CLI envelope onto capacity buckets: one bucket per
// model group, its 5-hour window primary and its weekly window secondary. The
// Gemini group is the overall bucket because the CLI's default models draw on
// it; every other group is additional.

func parseAgyUsage(raw []byte, observedAt time.Time) (ports.AgyCapacityObservation, error) {
	var envelope agyUsageEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ports.AgyCapacityObservation{}, fmt.Errorf("agy capacity: parse envelope: %w", ports.ErrAgyCapacityRequestRejected)
	}
	if !strings.EqualFold(strings.TrimSpace(envelope.Status), "SUCCESS") {
		if capacitySignedOut(envelope.Error) {
			return ports.AgyCapacityObservation{}, ports.ErrAgyCapacitySignedOut
		}
		return ports.AgyCapacityObservation{}, fmt.Errorf("agy capacity: status %q: %w", envelope.Status, ports.ErrAgyCapacityRequestRejected)
	}
	if envelope.Command == nil || len(envelope.Command.Data.Groups) == 0 {
		return ports.AgyCapacityObservation{}, fmt.Errorf("agy capacity: no usage groups: %w", ports.ErrAgyCapacityRequestRejected)
	}
	observation := ports.AgyCapacityObservation{ObservedAt: observedAt, AdditionalBuckets: []domain.AgyCapacityBucket{}}
	for _, group := range envelope.Command.Data.Groups {
		bucket, ok := capacityBucket(group)
		if !ok {
			continue
		}
		if observation.Overall == nil && strings.HasPrefix(bucket.LimitID, "gemini") {
			overall := bucket
			observation.Overall = &overall
			continue
		}
		observation.AdditionalBuckets = append(observation.AdditionalBuckets, bucket)
	}
	if observation.Overall == nil && len(observation.AdditionalBuckets) > 0 {
		overall := observation.AdditionalBuckets[0]
		observation.Overall = &overall
		observation.AdditionalBuckets = observation.AdditionalBuckets[1:]
	}
	if observation.Overall == nil {
		return ports.AgyCapacityObservation{}, fmt.Errorf("agy capacity: no usable windows: %w", ports.ErrAgyCapacityRequestRejected)
	}
	return observation, nil
}

func capacityBucket(group agyUsageGroup) (domain.AgyCapacityBucket, bool) {
	bucket := domain.AgyCapacityBucket{}
	if name := strings.TrimSpace(group.Name); name != "" {
		bucket.DisplayName = &name
	}
	for _, item := range group.Buckets {
		window := capacityWindow(item)
		if window == nil {
			continue
		}
		if bucket.LimitID == "" {
			bucket.LimitID = limitID(item.ID)
		}
		switch strings.ToLower(strings.TrimSpace(item.Window)) {
		case "5h":
			bucket.Primary = window
		case "weekly":
			bucket.Secondary = window
		}
	}
	if bucket.Primary == nil && bucket.Secondary == nil {
		return domain.AgyCapacityBucket{}, false
	}
	if bucket.LimitID == "" {
		bucket.LimitID = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(group.Name), " ", "-"))
	}
	return bucket, true
}

func capacityWindow(item agyUsageBucket) *domain.AgyCapacityWindow {
	var minutes int64
	switch strings.ToLower(strings.TrimSpace(item.Window)) {
	case "5h":
		minutes = 5 * 60
	case "weekly":
		minutes = 7 * 24 * 60
	default:
		return nil
	}
	used := (1 - item.RemainingFraction) * 100
	used = max(0, min(100, used))
	window := &domain.AgyCapacityWindow{UsedPercent: used, WindowDurationMinutes: &minutes}
	if resetsAt, err := time.Parse(time.RFC3339, strings.TrimSpace(item.ResetTime)); err == nil {
		resetsAt = resetsAt.UTC()
		window.ResetsAt = &resetsAt
	}
	return window
}

// limitID is the bucket id without its window suffix ("gemini-5h" → "gemini").
func limitID(bucketID string) string {
	id := strings.ToLower(strings.TrimSpace(bucketID))
	for _, suffix := range []string{"-5h", "-weekly"} {
		if strings.HasSuffix(id, suffix) {
			return strings.TrimSuffix(id, suffix)
		}
	}
	return id
}

func capacitySignedOut(text string) bool {
	if authenticationRequired(text) {
		return true
	}
	lower := strings.ToLower(text)
	for _, marker := range []string{"authentication required", "not signed in", "not logged in", "please visit the url", "sign in", "log in"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func environmentWithout(base, keys []string) []string {
	unset := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		unset[key] = struct{}{}
	}
	out := make([]string, 0, len(base))
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		if _, drop := unset[key]; !drop {
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

// limitedWriter keeps the first limit bytes so an unexpectedly chatty CLI
// cannot grow daemon memory.
type limitedWriter struct {
	buf   *bytes.Buffer
	limit int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if remaining := w.limit - w.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			w.buf.Write(p[:remaining])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}
