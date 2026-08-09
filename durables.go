package gobeyond

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Durable length budgets (ADR 006). Stricter than Temporal's 1000-byte ID limit.
const (
	MaxTaskQueueIDBytes = 48
	MaxEnvironmentBytes = 32
	MaxTaskQueueBytes   = 82 // taskQueueId + "__" + environment
	TaskQueueSeparator  = "__"
	DefaultTaskQueueID  = "default"
	LocalEnvironment    = "local"
	PreviewEnvironment  = "preview"
)

var durableNameCharset = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// NormalizeTaskQueueID validates and returns a logical task queue id.
// Empty becomes "default".
func NormalizeTaskQueueID(id string) (string, error) {
	id = strings.TrimSpace(strings.ToLower(id))
	if id == "" {
		id = DefaultTaskQueueID
	}
	if utf8.RuneCountInString(id) != len(id) {
		return "", fmt.Errorf("worker id %q must be ASCII", id)
	}
	if len(id) > MaxTaskQueueIDBytes {
		return "", fmt.Errorf("task queue id %q exceeds %d bytes", id, MaxTaskQueueIDBytes)
	}
	if !durableNameCharset.MatchString(id) {
		return "", fmt.Errorf("task queue id %q must match [a-z0-9]+(?:-[a-z0-9]+)*", id)
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

// TaskQueueName returns {taskQueueId}__{environment}.
func TaskQueueName(taskQueueID, environment string) (string, error) {
	taskQueueID, err := NormalizeTaskQueueID(taskQueueID)
	if err != nil {
		return "", err
	}
	environment, err = NormalizeEnvironment(environment)
	if err != nil {
		return "", err
	}
	queue := taskQueueID + TaskQueueSeparator + environment
	if len(queue) > MaxTaskQueueBytes {
		return "", fmt.Errorf("task queue %q exceeds %d bytes", queue, MaxTaskQueueBytes)
	}
	return queue, nil
}
