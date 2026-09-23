package temporal

import (
	"context"
	"sync"

	"go.temporal.io/sdk/worker"
)

// recoverySlots closes intake before checking occupancy. Reservations include
// long polls and eager tasks, not just activities that reached user code.
// Each SDK worker incarnation gets fresh suppliers; a closed one never reopens.
type recoverySlots struct {
	mu        sync.Mutex
	capacity  int
	available chan struct{}
	closing   chan struct{}
	closed    bool
	permits   map[*worker.SlotPermit]bool // false=reserved, true=executing
}

func newRecoverySlots(capacity int) *recoverySlots {
	if capacity < 1 {
		panic("positive slot capacity required")
	}
	return &recoverySlots{capacity: capacity, available: make(chan struct{}, capacity), closing: make(chan struct{}), permits: map[*worker.SlotPermit]bool{}}
}
func (s *recoverySlots) ReserveSlot(ctx context.Context, _ worker.SlotReservationInfo) (*worker.SlotPermit, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closing:
		return nil, context.Canceled
	case s.available <- struct{}{}:
	}
	return s.issue()
}
func (s *recoverySlots) TryReserveSlot(_ worker.SlotReservationInfo) *worker.SlotPermit {
	select {
	case <-s.closing:
		return nil
	case s.available <- struct{}{}:
	default:
		return nil
	}
	permit, _ := s.issue()
	return permit
}
func (s *recoverySlots) issue() (*worker.SlotPermit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		<-s.available
		return nil, context.Canceled
	}
	p := &worker.SlotPermit{}
	s.permits[p] = false
	return p, nil
}
func (s *recoverySlots) MarkSlotUsed(info worker.SlotMarkUsedInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// An acquired task can arrive after intake closes. It remains accounted for
	// until SDK shutdown releases it; never reject it as if it had not arrived.
	if _, ok := s.permits[info.Permit()]; ok {
		s.permits[info.Permit()] = true
	}
}
func (s *recoverySlots) ReleaseSlot(info worker.SlotReleaseInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.permits[info.Permit()]; ok {
		delete(s.permits, info.Permit())
		<-s.available
	}
}
func (s *recoverySlots) MaxSlots() int { return s.capacity }
func (s *recoverySlots) closeIntake() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.closing)
	}
}
func (s *recoverySlots) snapshot() (reserved, executing int, closed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, used := range s.permits {
		if used {
			executing++
		} else {
			reserved++
		}
	}
	return reserved, executing, s.closed
}

type recoveryTuner struct{ suppliers [5]*recoverySlots }

func newRecoveryTuner(activitySlots int) (*recoveryTuner, error) {
	// Read defaults through the SDK instead of copying its workflow/local limits.
	defaults, err := worker.NewFixedSizeTuner(worker.FixedSizeTunerOptions{NumActivitySlots: activitySlots})
	if err != nil {
		return nil, err
	}
	source := []worker.SlotSupplier{defaults.GetWorkflowTaskSlotSupplier(), defaults.GetActivityTaskSlotSupplier(), defaults.GetLocalActivitySlotSupplier(), defaults.GetNexusSlotSupplier(), defaults.GetSessionActivitySlotSupplier()}
	tuner := &recoveryTuner{}
	for i, s := range source {
		if s != nil {
			tuner.suppliers[i] = newRecoverySlots(s.MaxSlots())
		}
	}
	return tuner, nil
}
func (t *recoveryTuner) GetWorkflowTaskSlotSupplier() worker.SlotSupplier  { return t.suppliers[0] }
func (t *recoveryTuner) GetActivityTaskSlotSupplier() worker.SlotSupplier  { return t.suppliers[1] }
func (t *recoveryTuner) GetLocalActivitySlotSupplier() worker.SlotSupplier { return t.suppliers[2] }
func (t *recoveryTuner) GetNexusSlotSupplier() worker.SlotSupplier         { return t.suppliers[3] }
func (t *recoveryTuner) GetSessionActivitySlotSupplier() worker.SlotSupplier {
	if t.suppliers[4] == nil {
		return nil
	}
	return t.suppliers[4]
}
func (t *recoveryTuner) closeIntake() {
	for _, s := range t.suppliers {
		if s != nil {
			s.closeIntake()
		}
	}
}
func (t *recoveryTuner) drained() bool {
	for _, s := range t.suppliers {
		if s != nil {
			r, e, closed := s.snapshot()
			if !closed || r+e != 0 {
				return false
			}
		}
	}
	return true
}

// accountedAfterStop may only be called after Worker.Stop returns. The SDK
// can retain a workflow-task permit abandoned during local-activity retry;
// Temporal must time that task out and replay it. It is safe to exit only when
// independent execution coverage was confirmed before quiescing. Activities,
// local activities and outstanding polls must still drain; they cannot be
// classified as abandoned workflow-task bookkeeping.
func (t *recoveryTuner) accountedAfterStop(coverageConfirmed bool) (abandonedWorkflowTasks int, safe bool) {
	if !coverageConfirmed {
		return 0, false
	}
	for i, s := range t.suppliers {
		if s == nil {
			continue
		}
		reserved, executing, closed := s.snapshot()
		if !closed || reserved != 0 {
			return 0, false
		}
		if i == 0 {
			abandonedWorkflowTasks = executing
		} else if executing != 0 {
			return 0, false
		}
	}
	return abandonedWorkflowTasks, true
}
