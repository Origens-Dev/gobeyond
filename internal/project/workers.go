package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gb "github.com/Origens-Dev/gobeyond"
)

// Worker describes one authored durables unit under workers/.
type Worker struct {
	// ID is the queue leaf (folder name or "default").
	ID string
	// Key is the Go-safe generated package directory name.
	Key string
	// DurablesFile is slash-separated relative to the website root.
	DurablesFile string
}

// DiscoverWorkers finds workers/<id>/durables.go.
// Allowed layout:
//   - workers/<name>/durables.go → id <name> (use workers/default/ for the default worker)
//
// Root workers/durables.go is rejected. Nested durables deeper than one folder
// under workers/ are also rejected.
func DiscoverWorkers(root string) ([]Worker, error) {
	workersRoot := filepath.Join(root, "workers")
	info, err := os.Stat(workersRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("workers must be a directory")
	}

	var workers []Worker
	err = filepath.WalkDir(workersRoot, func(file string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "durables.go" {
			return nil
		}
		authorDir := filepath.Dir(file)
		rel, relErr := filepath.Rel(workersRoot, authorDir)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		var id string
		switch {
		case rel == ".":
			return errors.New("workers/durables.go is not allowed; use workers/<id>/durables.go (e.g. workers/default/durables.go)")
		case !strings.Contains(rel, "/"):
			id = rel
		default:
			return fmt.Errorf("workers/%s/durables.go is nested too deeply; use workers/<name>/durables.go", rel)
		}
		normalized, normErr := gb.NormalizeWorkerID(id)
		if normErr != nil {
			return fmt.Errorf("workers/%s: %w", rel, normErr)
		}
		relativeFile, fileErr := filepath.Rel(root, file)
		if fileErr != nil {
			return fileErr
		}
		workers = append(workers, Worker{
			ID:           normalized,
			Key:          WorkerKey(normalized),
			DurablesFile: filepath.ToSlash(relativeFile),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	seen := map[string]string{}
	for _, worker := range workers {
		if previous, ok := seen[worker.ID]; ok {
			return nil, fmt.Errorf("duplicate worker id %q from %s and %s", worker.ID, previous, worker.DurablesFile)
		}
		seen[worker.ID] = worker.DurablesFile
	}
	return workers, nil
}

// WorkerKey returns the deterministic Go-safe package directory for a worker id.
func WorkerKey(workerID string) string {
	digest := sha256.Sum256([]byte("worker:" + workerID))
	name := strings.Trim(safePart(workerID), "_")
	if name == "" {
		name = "worker"
	}
	return "w_" + name + "_" + hex.EncodeToString(digest[:4])
}
