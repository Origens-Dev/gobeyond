package gobeyond

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Durable length budgets (ADR 006). Stricter than Temporal's 1000-byte ID limit.
const (
	MaxWorkerIDBytes     = 48
	MaxEnvironmentBytes  = 32
	MaxTaskQueueBytes    = 82 // workerId + "__" + environment
	TaskQueueSeparator   = "__"
	DefaultWorkerID      = "default"
	LocalEnvironment     = "local"
	PreviewEnvironment   = "preview"
)

var durableNameCharset = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// TaskConfig declares compiler-visible metadata for a standalone durable task
// (Temporal activity in the Temporal adapter). Authors do not set a full task
// queue name; the platform resolves {workerId}__{environment}.
type TaskConfig struct {
	Name    string
	Timeout time.Duration
}

// WorkflowConfig declares compiler-visible metadata for a durable workflow.
type WorkflowConfig struct {
	Name             string
	ExecutionTimeout time.Duration
}

// NormalizeWorkerID validates and returns a worker id suitable for queue names.
// Empty becomes "default".
func NormalizeWorkerID(id string) (string, error) {
	id = strings.TrimSpace(strings.ToLower(id))
	if id == "" {
		id = DefaultWorkerID
	}
	if utf8.RuneCountInString(id) != len(id) {
		return "", fmt.Errorf("worker id %q must be ASCII", id)
	}
	if len(id) > MaxWorkerIDBytes {
		return "", fmt.Errorf("worker id %q exceeds %d bytes", id, MaxWorkerIDBytes)
	}
	if !durableNameCharset.MatchString(id) {
		return "", fmt.Errorf("worker id %q must match [a-z0-9]+(?:-[a-z0-9]+)*", id)
	}
	return id, nil
}

// NormalizeEnvironment validates an environment slug used in task queue names.
func NormalizeEnvironment(env string) (string, error) {
	env = strings.TrimSpace(strings.ToLower(env))
	if env == "" {
		return "", fmt.Errorf("environment must not be empty")
	}
	if utf8.RuneCountInString(env) != len(env) {
		return "", fmt.Errorf("environment %q must be ASCII", env)
	}
	if len(env) > MaxEnvironmentBytes {
		return "", fmt.Errorf("environment %q exceeds %d bytes", env, MaxEnvironmentBytes)
	}
	if !durableNameCharset.MatchString(env) {
		return "", fmt.Errorf("environment %q must match [a-z0-9]+(?:-[a-z0-9]+)*", env)
	}
	return env, nil
}

// TaskQueueName returns {workerId}__{environment}.
func TaskQueueName(workerID, environment string) (string, error) {
	workerID, err := NormalizeWorkerID(workerID)
	if err != nil {
		return "", err
	}
	environment, err = NormalizeEnvironment(environment)
	if err != nil {
		return "", err
	}
	queue := workerID + TaskQueueSeparator + environment
	if len(queue) > MaxTaskQueueBytes {
		return "", fmt.Errorf("task queue %q exceeds %d bytes", queue, MaxTaskQueueBytes)
	}
	return queue, nil
}
