package temporal

import (
	"context"
	"go.temporal.io/sdk/worker"
	"sync"
	"testing"
)

type recoveryMark struct {
	worker.SlotMarkUsedInfo
	p *worker.SlotPermit
}

func (m recoveryMark) Permit() *worker.SlotPermit { return m.p }

type recoveryRelease struct {
	worker.SlotReleaseInfo
	p *worker.SlotPermit
}

func (m recoveryRelease) Permit() *worker.SlotPermit { return m.p }
func TestRecoveryQuiesceAccountsForLateAcquiredTask(t *testing.T) {
	s := newRecoverySlots(2)
	p, err := s.ReserveSlot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.closeIntake()
	s.MarkSlotUsed(recoveryMark{p: p})
	r, e, closed := s.snapshot()
	if !closed || r != 0 || e != 1 {
		t.Fatal(r, e, closed)
	}
	if s.TryReserveSlot(nil) != nil {
		t.Fatal("accepted eager work after close")
	}
	if _, err := s.ReserveSlot(context.Background(), nil); err == nil {
		t.Fatal("accepted poll after close")
	}
	s.ReleaseSlot(recoveryRelease{p: p})
	s.ReleaseSlot(recoveryRelease{p: p})
	r, e, _ = s.snapshot()
	if r+e != 0 {
		t.Fatal("release leaked", r, e)
	}
}
func TestRecoveryQuiesceRace(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		s := newRecoverySlots(8)
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p, err := s.ReserveSlot(context.Background(), nil)
				if err == nil {
					s.MarkSlotUsed(recoveryMark{p: p})
					s.ReleaseSlot(recoveryRelease{p: p})
				}
			}()
		}
		s.closeIntake()
		wg.Wait()
		r, e, _ := s.snapshot()
		if r+e != 0 {
			t.Fatal("quiesce leaked permits")
		}
	}
}
func TestRecoveryTunerRequiresEveryTaskKindDrained(t *testing.T) {
	tuner, err := newRecoveryTuner(100)
	if err != nil {
		t.Fatal(err)
	}
	suppliers := []worker.SlotSupplier{tuner.GetWorkflowTaskSlotSupplier(), tuner.GetActivityTaskSlotSupplier(), tuner.GetLocalActivitySlotSupplier()}
	permits := make([]*worker.SlotPermit, len(suppliers))
	for i, s := range suppliers {
		permits[i], err = s.ReserveSlot(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	tuner.closeIntake()
	for i, s := range suppliers {
		if tuner.drained() {
			t.Fatal("acknowledged before all task kinds drained")
		}
		s.ReleaseSlot(recoveryRelease{p: permits[i]})
	}
	if !tuner.drained() {
		t.Fatal("empty closed tuner not drained")
	}
	if tuner.GetActivityTaskSlotSupplier().MaxSlots() != 100 {
		t.Fatal("changed activity concurrency")
	}
}
