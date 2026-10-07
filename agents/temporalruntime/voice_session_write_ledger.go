package temporalruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const voiceWriteLedgerDirEnv = "GOBEYOND_VOICE_WRITE_LEDGER_DIR"

type voiceWriteRecord struct {
	key      string
	digest   string
	result   VoiceSessionExecuteToolResult
	complete bool
	unknown  bool
	metered  bool
	wait     chan struct{}
}

type voiceWritePersistedRecord struct {
	Identity string                        `json:"identity"`
	Key      string                        `json:"key"`
	Digest   string                        `json:"digest"`
	Result   VoiceSessionExecuteToolResult `json:"result"`
	Complete bool                          `json:"complete"`
	Unknown  bool                          `json:"unknown"`
	Metered  bool                          `json:"metered"`
}

type voiceWriteLedger struct {
	mu      sync.Mutex
	dir     string
	records map[string]voiceWriteRecord
}

func newVoiceWriteLedger() *voiceWriteLedger {
	return &voiceWriteLedger{records: map[string]voiceWriteRecord{}}
}

func openVoiceWriteLedger(dir string) *voiceWriteLedger {
	return &voiceWriteLedger{dir: strings.TrimSpace(dir), records: map[string]voiceWriteRecord{}}
}

func defaultVoiceWriteLedgerDir() string {
	if dir := strings.TrimSpace(os.Getenv(voiceWriteLedgerDirEnv)); dir != "" {
		return dir
	}
	cache, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cache) == "" {
		cache = os.TempDir()
	}
	return filepath.Join(cache, "gobeyond", "voice-write-ledger")
}

var processWriteLedger = openVoiceWriteLedger(defaultVoiceWriteLedgerDir())

func resetVoiceWriteLedger() { processWriteLedger = newVoiceWriteLedger() }

func resetVoiceWriteLedgerWithDir(dir string) {
	processWriteLedger = openVoiceWriteLedger(dir)
}

// reopenVoiceWriteLedger drops the process-local cache and reopens the same
// durable directory, simulating a replacement worker.
func reopenVoiceWriteLedger() {
	dir := ""
	if processWriteLedger != nil {
		dir = processWriteLedger.dir
	}
	processWriteLedger = openVoiceWriteLedger(dir)
}

func (l *voiceWriteLedger) dispatch(identity, key, digest string, lookupOnly bool, run func() (VoiceSessionExecuteToolResult, error)) (VoiceSessionExecuteToolResult, error) {
	for {
		l.mu.Lock()
		rec, ok, err := l.loadLocked(identity)
		if err != nil {
			l.mu.Unlock()
			return VoiceSessionExecuteToolResult{}, errWriteOutcomeUnknown
		}
		if ok {
			if rec.key != key || rec.digest != digest {
				l.mu.Unlock()
				return VoiceSessionExecuteToolResult{}, errWriteConflict
			}
			if rec.complete {
				result := rec.result
				l.mu.Unlock()
				return result, nil
			}
			if rec.wait != nil {
				wait := rec.wait
				l.mu.Unlock()
				<-wait
				continue
			}
			l.mu.Unlock()
			return VoiceSessionExecuteToolResult{}, errWriteOutcomeUnknown
		}
		if lookupOnly {
			l.mu.Unlock()
			return VoiceSessionExecuteToolResult{}, errWriteOutcomeUnknown
		}
		wait := make(chan struct{})
		rec = voiceWriteRecord{key: key, digest: digest, wait: wait}
		if err := l.reserveLocked(identity, rec); err != nil {
			existing, loaded, loadErr := l.loadDurableLocked(identity)
			l.mu.Unlock()
			if loadErr == nil && loaded {
				if existing.key != key || existing.digest != digest {
					return VoiceSessionExecuteToolResult{}, errWriteConflict
				}
				if existing.complete {
					return existing.result, nil
				}
			}
			return VoiceSessionExecuteToolResult{}, errWriteOutcomeUnknown
		}
		l.records[identity] = rec
		l.mu.Unlock()

		result, runErr := run()

		l.mu.Lock()
		finished := voiceWriteRecord{key: key, digest: digest, metered: true}
		if runErr != nil {
			finished.unknown = true
		} else {
			finished.result = result
			finished.complete = true
		}
		l.records[identity] = finished
		_ = l.persistLocked(identity, finished)
		close(wait)
		l.mu.Unlock()
		if runErr != nil {
			return VoiceSessionExecuteToolResult{}, runErr
		}
		return result, nil
	}
}

func (l *voiceWriteLedger) loadLocked(identity string) (voiceWriteRecord, bool, error) {
	if rec, ok := l.records[identity]; ok {
		return rec, true, nil
	}
	return l.loadDurableLocked(identity)
}

func (l *voiceWriteLedger) recordPath(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return filepath.Join(l.dir, hex.EncodeToString(sum[:])+".json")
}

func (l *voiceWriteLedger) loadDurableLocked(identity string) (voiceWriteRecord, bool, error) {
	if l == nil || l.dir == "" {
		return voiceWriteRecord{}, false, nil
	}
	raw, err := os.ReadFile(l.recordPath(identity))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return voiceWriteRecord{}, false, nil
		}
		return voiceWriteRecord{}, false, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		rec := voiceWriteRecord{unknown: true}
		l.records[identity] = rec
		return rec, true, nil
	}
	var persisted voiceWritePersistedRecord
	if err := json.Unmarshal(raw, &persisted); err != nil {
		rec := voiceWriteRecord{unknown: true}
		l.records[identity] = rec
		return rec, true, nil
	}
	rec := voiceWriteRecord{
		key:      persisted.Key,
		digest:   persisted.Digest,
		result:   persisted.Result,
		complete: persisted.Complete,
		unknown:  persisted.Unknown,
		metered:  persisted.Metered,
	}
	l.records[identity] = rec
	return rec, true, nil
}

func (l *voiceWriteLedger) reserveLocked(identity string, rec voiceWriteRecord) error {
	if l == nil || l.dir == "" {
		return nil
	}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return err
	}
	return l.writeRecord(l.recordPath(identity), identity, rec, true)
}

func (l *voiceWriteLedger) persistLocked(identity string, rec voiceWriteRecord) error {
	if l == nil || l.dir == "" {
		return nil
	}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return err
	}
	return l.writeRecord(l.recordPath(identity), identity, rec, false)
}

func (l *voiceWriteLedger) writeRecord(path, identity string, rec voiceWriteRecord, exclusive bool) error {
	raw, err := json.Marshal(voiceWritePersistedRecord{
		Identity: identity,
		Key:      rec.key,
		Digest:   rec.digest,
		Result:   rec.result,
		Complete: rec.complete,
		Unknown:  rec.unknown,
		Metered:  rec.metered,
	})
	if err != nil {
		return err
	}
	if exclusive {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = f.Write(raw)
		if err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
			return err
		}
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, err = tmp.Write(raw)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func seedVoiceWriteUnknown(identity, key, digest string) {
	if processWriteLedger == nil {
		resetVoiceWriteLedger()
	}
	processWriteLedger.mu.Lock()
	defer processWriteLedger.mu.Unlock()
	rec := voiceWriteRecord{key: key, digest: digest, unknown: true}
	processWriteLedger.records[identity] = rec
	_ = processWriteLedger.persistLocked(identity, rec)
}

func voiceWriteLedgerState(identity string) (complete, unknown, metered bool, result VoiceSessionExecuteToolResult) {
	if processWriteLedger == nil {
		return false, false, false, VoiceSessionExecuteToolResult{}
	}
	processWriteLedger.mu.Lock()
	defer processWriteLedger.mu.Unlock()
	rec, ok, err := processWriteLedger.loadLocked(identity)
	if err != nil || !ok {
		return false, false, false, VoiceSessionExecuteToolResult{}
	}
	return rec.complete, rec.unknown, rec.metered, rec.result
}
