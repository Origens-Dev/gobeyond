package temporalruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const voiceWriteLedgerDirEnv = "GOBEYOND_VOICE_WRITE_LEDGER_DIR"

var errWriteLedgerPersist = errors.New("write ledger persist failed")

type voiceWriteRecord struct {
	key      string
	digest   string
	result   VoiceSessionExecuteToolResult
	complete bool
	unknown  bool
	metered  bool
	wait     chan struct{}
}

// VoiceWritePersistedRecord is the durable ledger row for one write identity.
// Shared-authority adapters (hosted persistence / product receipt lookup) load
// and store this shape; Key is the platform ToolWriteID / idempotency digest.
type VoiceWritePersistedRecord struct {
	Identity string                        `json:"identity"`
	Key      string                        `json:"key"`
	Digest   string                        `json:"digest"`
	Result   VoiceSessionExecuteToolResult `json:"result"`
	Complete bool                          `json:"complete"`
	Unknown  bool                          `json:"unknown"`
	Metered  bool                          `json:"metered"`
}

func (r voiceWriteRecord) persisted(identity string) VoiceWritePersistedRecord {
	return VoiceWritePersistedRecord{
		Identity: identity,
		Key:      r.key,
		Digest:   r.digest,
		Result:   r.result,
		Complete: r.complete,
		Unknown:  r.unknown,
		Metered:  r.metered,
	}
}

func (p VoiceWritePersistedRecord) record() voiceWriteRecord {
	rec := voiceWriteRecord{
		key:      p.Key,
		digest:   p.Digest,
		result:   p.Result,
		complete: p.Complete,
		unknown:  p.Unknown,
		metered:  p.Metered,
	}
	// Never treat a tool error as durable success, including legacy rows that
	// cached Error under complete=true after a product mutation.
	if rec.complete && voiceWriteResultUncertain(rec.result) {
		rec.complete = false
		rec.unknown = true
		rec.result = VoiceSessionExecuteToolResult{}
	}
	return rec
}

// voiceWriteResultUncertain reports outcomes that must stay on the
// pending/unknown reconcile path instead of sticky completed-failure.
func voiceWriteResultUncertain(result VoiceSessionExecuteToolResult) bool {
	return strings.TrimSpace(result.Error) != ""
}

// VoiceWriteStore is the shared authoritative write-result SoR used by
// reconcile after host/container replacement with empty local storage.
// Host-local files only cover process restart on the same disk. Production
// workers attach an implementation with RetainVoiceWriteAuthority (for
// example product durable receipt lookup keyed by ToolWriteID). Product
// handlers still durably dedupe by ToolWriteID.
type VoiceWriteStore interface {
	Load(identity string) (VoiceWritePersistedRecord, bool, error)
	Reserve(identity string, rec VoiceWritePersistedRecord) error
	Persist(identity string, rec VoiceWritePersistedRecord) error
}

type memoryVoiceWriteStore struct {
	mu      sync.Mutex
	records map[string]VoiceWritePersistedRecord
}

func newMemoryVoiceWriteStore() *memoryVoiceWriteStore {
	return &memoryVoiceWriteStore{records: map[string]VoiceWritePersistedRecord{}}
}

func (s *memoryVoiceWriteStore) Load(identity string) (VoiceWritePersistedRecord, bool, error) {
	if s == nil {
		return VoiceWritePersistedRecord{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[identity]
	return rec, ok, nil
}

func (s *memoryVoiceWriteStore) Reserve(identity string, rec VoiceWritePersistedRecord) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[identity]; ok {
		return os.ErrExist
	}
	s.records[identity] = rec
	return nil
}

func (s *memoryVoiceWriteStore) Persist(identity string, rec VoiceWritePersistedRecord) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[identity] = rec
	return nil
}

type fileVoiceWriteStore struct {
	dir string
}

func (s *fileVoiceWriteStore) recordPath(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

func (s *fileVoiceWriteStore) Load(identity string) (VoiceWritePersistedRecord, bool, error) {
	if s == nil || s.dir == "" {
		return VoiceWritePersistedRecord{}, false, nil
	}
	raw, err := os.ReadFile(s.recordPath(identity))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return VoiceWritePersistedRecord{}, false, nil
		}
		return VoiceWritePersistedRecord{}, false, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return VoiceWritePersistedRecord{Identity: identity, Unknown: true}, true, nil
	}
	var persisted VoiceWritePersistedRecord
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return VoiceWritePersistedRecord{Identity: identity, Unknown: true}, true, nil
	}
	return persisted, true, nil
}

func (s *fileVoiceWriteStore) Reserve(identity string, rec VoiceWritePersistedRecord) error {
	if s == nil || s.dir == "" {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return s.writeRecord(s.recordPath(identity), rec, true)
}

func (s *fileVoiceWriteStore) Persist(identity string, rec VoiceWritePersistedRecord) error {
	if s == nil || s.dir == "" {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return s.writeRecord(s.recordPath(identity), rec, false)
}

func (s *fileVoiceWriteStore) writeRecord(path string, rec VoiceWritePersistedRecord, exclusive bool) error {
	raw, err := json.Marshal(rec)
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

type voiceWriteLedger struct {
	mu      sync.Mutex
	dir     string
	local   VoiceWriteStore
	shared  VoiceWriteStore
	records map[string]voiceWriteRecord
}

func newVoiceWriteLedger() *voiceWriteLedger {
	return &voiceWriteLedger{records: map[string]voiceWriteRecord{}}
}

func openVoiceWriteLedger(dir string) *voiceWriteLedger {
	return openVoiceWriteLedgerWithAuthority(dir, nil)
}

func openVoiceWriteLedgerWithAuthority(dir string, shared VoiceWriteStore) *voiceWriteLedger {
	dir = strings.TrimSpace(dir)
	l := &voiceWriteLedger{dir: dir, shared: shared, records: map[string]voiceWriteRecord{}}
	if dir != "" {
		l.local = &fileVoiceWriteStore{dir: dir}
	}
	return l
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

func resetVoiceWriteLedgerWithAuthority(dir string, shared VoiceWriteStore) {
	processWriteLedger = openVoiceWriteLedgerWithAuthority(dir, shared)
}

func resetVoiceWriteLedgerStores(local, shared VoiceWriteStore) {
	processWriteLedger = &voiceWriteLedger{local: local, shared: shared, records: map[string]voiceWriteRecord{}}
}

// RetainVoiceWriteAuthority attaches the shared authoritative write store for
// this process. Production workers must call this once at startup (alongside
// RetainVoiceRegistry) so a replacement host with empty local storage can
// reconcile through hosted persistence or product durable receipt lookup.
// Passing nil clears the shared store and keeps only the host-local replica.
// The process-local in-flight cache is dropped so the next load consults the
// newly attached authority.
func RetainVoiceWriteAuthority(store VoiceWriteStore) {
	dir := defaultVoiceWriteLedgerDir()
	if processWriteLedger != nil && strings.TrimSpace(processWriteLedger.dir) != "" {
		dir = processWriteLedger.dir
	}
	processWriteLedger = openVoiceWriteLedgerWithAuthority(dir, store)
}

// ProcessVoiceWriteAuthority returns the shared store retained for this
// process, if any.
func ProcessVoiceWriteAuthority() VoiceWriteStore {
	if processWriteLedger == nil {
		return nil
	}
	return processWriteLedger.shared
}

// reopenVoiceWriteLedger drops the process-local cache and reopens the same
// host-local directory, simulating a process restart on the same disk.
func reopenVoiceWriteLedger() {
	dir, shared := "", VoiceWriteStore(nil)
	if processWriteLedger != nil {
		dir = processWriteLedger.dir
		shared = processWriteLedger.shared
	}
	processWriteLedger = openVoiceWriteLedgerWithAuthority(dir, shared)
}

// replaceHostVoiceWriteLedger drops process cache and host-local files, keeping
// the shared authoritative store. This is a replacement worker on a new host.
func replaceHostVoiceWriteLedger(dir string) {
	var shared VoiceWriteStore
	if processWriteLedger != nil {
		shared = processWriteLedger.shared
	}
	processWriteLedger = openVoiceWriteLedgerWithAuthority(dir, shared)
}

// ReplaceHostVoiceWriteLedger is the exported fresh-host restart for product
// proofs: empty host-local directory, process cache dropped, shared
// VoiceWriteStore (RetainVoiceWriteAuthority) retained.
func ReplaceHostVoiceWriteLedger(dir string) {
	replaceHostVoiceWriteLedger(dir)
}

func voiceWriteAuthorityStore() VoiceWriteStore {
	return ProcessVoiceWriteAuthority()
}

// forgetVoiceWriteProcessRecord drops a sticky process-local row so the next
// load consults durable authority again. Used when a recovered "complete"
// result fails frozen output validation: the invalid payload must not remain
// cached as durable complete and hide a later authority repair.
func forgetVoiceWriteProcessRecord(identity string) {
	if processWriteLedger == nil || identity == "" {
		return
	}
	processWriteLedger.mu.Lock()
	defer processWriteLedger.mu.Unlock()
	delete(processWriteLedger.records, identity)
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
			if rec.complete && !voiceWriteResultUncertain(rec.result) {
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
				if existing.complete && !voiceWriteResultUncertain(existing.result) {
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
		uncertain := runErr != nil || voiceWriteResultUncertain(result)
		if uncertain {
			// Tool errors (including after a product mutation already committed)
			// stay reconcilable: never sticky-cache completed-failure.
			finished.unknown = true
		} else {
			finished.result = result
			finished.complete = true
		}
		if err := l.persistLocked(identity, finished); err != nil {
			// Fail closed on the final success persist: never return durable
			// success. Best-effort mark unknown for reconcile; if that mark
			// also fails, the exclusive reservation left pending is still
			// enough for a replacement host to refuse re-execute.
			failed := voiceWriteRecord{key: key, digest: digest, unknown: true, metered: true}
			l.records[identity] = failed
			_ = l.persistLocked(identity, failed)
			close(wait)
			l.mu.Unlock()
			return VoiceSessionExecuteToolResult{}, fmt.Errorf("%w: %w", errWriteOutcomeUnknown, err)
		}
		l.records[identity] = finished
		close(wait)
		l.mu.Unlock()
		if uncertain {
			if runErr != nil {
				return VoiceSessionExecuteToolResult{}, fmt.Errorf("%w: %w", errWriteOutcomeUnknown, runErr)
			}
			return VoiceSessionExecuteToolResult{}, fmt.Errorf("%w: %s", errWriteOutcomeUnknown, strings.TrimSpace(result.Error))
		}
		return result, nil
	}
}

func (l *voiceWriteLedger) loadLocked(identity string) (voiceWriteRecord, bool, error) {
	if rec, ok := l.records[identity]; ok {
		// Durable success and in-flight waits stay process-local. Pending,
		// unknown, and completed-failure rows must re-query durable authority
		// so another worker's completed result becomes visible.
		certainComplete := rec.complete && !voiceWriteResultUncertain(rec.result)
		if certainComplete || rec.wait != nil {
			return rec, true, nil
		}
		refreshed, found, err := l.loadDurableLocked(identity)
		if err != nil {
			return voiceWriteRecord{}, false, err
		}
		if found {
			return refreshed, true, nil
		}
		// Authority miss after a prior pending/unknown: keep fail-closed.
		return rec, true, nil
	}
	return l.loadDurableLocked(identity)
}

func (l *voiceWriteLedger) loadDurableLocked(identity string) (voiceWriteRecord, bool, error) {
	if l == nil {
		return voiceWriteRecord{}, false, nil
	}
	if l.shared != nil {
		persisted, ok, err := l.shared.Load(identity)
		if err != nil {
			return voiceWriteRecord{}, false, err
		}
		if ok {
			rec := persisted.record()
			l.records[identity] = rec
			return rec, true, nil
		}
	}
	if l.local != nil {
		persisted, ok, err := l.local.Load(identity)
		if err != nil {
			return voiceWriteRecord{}, false, err
		}
		if ok {
			rec := persisted.record()
			l.records[identity] = rec
			return rec, true, nil
		}
	}
	return voiceWriteRecord{}, false, nil
}

func (l *voiceWriteLedger) reserveLocked(identity string, rec voiceWriteRecord) error {
	persisted := rec.persisted(identity)
	if l != nil && l.shared != nil {
		if err := l.shared.Reserve(identity, persisted); err != nil {
			return err
		}
	}
	if l != nil && l.local != nil {
		if err := l.local.Reserve(identity, persisted); err != nil {
			if l.shared != nil {
				return nil
			}
			return err
		}
	}
	return nil
}

func (l *voiceWriteLedger) persistLocked(identity string, rec voiceWriteRecord) error {
	persisted := rec.persisted(identity)
	if l != nil && l.shared != nil {
		if err := l.shared.Persist(identity, persisted); err != nil {
			return fmt.Errorf("%w: %w", errWriteLedgerPersist, err)
		}
	}
	if l != nil && l.local != nil {
		if err := l.local.Persist(identity, persisted); err != nil {
			if l.shared != nil {
				return nil
			}
			return fmt.Errorf("%w: %w", errWriteLedgerPersist, err)
		}
	}
	return nil
}

func seedVoiceWriteUnknown(identity, key, digest string) error {
	if processWriteLedger == nil {
		resetVoiceWriteLedger()
	}
	processWriteLedger.mu.Lock()
	defer processWriteLedger.mu.Unlock()
	rec := voiceWriteRecord{key: key, digest: digest, unknown: true}
	processWriteLedger.records[identity] = rec
	return processWriteLedger.persistLocked(identity, rec)
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
