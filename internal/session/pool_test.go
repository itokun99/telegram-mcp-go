package session

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSessionPool(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", " \n\t ", nil},
		{"mixed separators and duplicates", " a\tb, c ;;a\nd ", []string{"a", "b", "c", "d"}},
		{"single", "solo", []string{"solo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseSessionPool(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("ParseSessionPool(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ParseSessionPool(%q) = %q, want %q", tc.raw, got, tc.want)
				}
			}
		})
	}
}

func TestClaimSlotSkipsClaimedSlotsAndRefusesWhenExhausted(t *testing.T) {
	dir := t.TempDir()

	// Simulate another process holding slot A with an independent lock
	// handle: distinct open file descriptions conflict under flock/LockFileEx
	// even inside one process, exactly like another process would.
	foreign, err := NewSessionLockIn("foreign", StringSessionIdentity("slot-A"), dir)
	if err != nil {
		t.Fatalf("NewSessionLockIn: %v", err)
	}
	taken, err := foreign.TryOnce()
	if err != nil || !taken {
		t.Fatalf("foreign claim of slot A: taken=%v err=%v, want true", taken, err)
	}
	defer foreign.Release()

	slotB, err := claimSlotIn(dir, "slot-A slot-B slot-C")
	if err != nil {
		t.Fatalf("claiming with slot A taken: %v", err)
	}
	if slotB.Index != 1 || slotB.Session != "slot-B" {
		t.Fatalf("claimed slot %d/%q, want slot 1/slot-B", slotB.Index, slotB.Session)
	}
	if !slotB.Lock.Holding() {
		t.Fatal("claimed slot does not hold its lock")
	}

	slotC, err := claimSlotIn(dir, "slot-A slot-B slot-C")
	if err != nil {
		t.Fatalf("claiming the next free slot: %v", err)
	}
	if slotC.Index != 2 || slotC.Session != "slot-C" {
		t.Fatalf("claimed slot %d/%q, want slot 2/slot-C", slotC.Index, slotC.Session)
	}

	var exhausted *PoolExhaustedError
	slot, err := claimSlotIn(dir, "slot-A slot-B slot-C")
	if slot != nil {
		t.Fatal("pool exhaustion must not hand out a slot")
	}
	if !errors.As(err, &exhausted) {
		t.Fatalf("claiming with every slot taken: want *PoolExhaustedError, got %#v", err)
	}
	if exhausted.Slots != 3 {
		t.Fatalf("PoolExhaustedError.Slots = %d, want 3", exhausted.Slots)
	}
}

func TestClaimSlotRefusesSingleClaimedSlot(t *testing.T) {
	dir := t.TempDir()
	foreign, err := NewSessionLockIn("foreign", StringSessionIdentity("only"), dir)
	if err != nil {
		t.Fatalf("NewSessionLockIn: %v", err)
	}
	if taken, err := foreign.TryOnce(); err != nil || !taken {
		t.Fatalf("foreign claim: taken=%v err=%v, want true", taken, err)
	}
	defer foreign.Release()

	var exhausted *PoolExhaustedError
	slot, err := claimSlotIn(dir, "only")
	if slot != nil {
		t.Fatal("a claimed session was handed out")
	}
	if !errors.As(err, &exhausted) {
		t.Fatalf("want *PoolExhaustedError, got %#v", err)
	}
	if exhausted.Slots != 1 {
		t.Fatalf("PoolExhaustedError.Slots = %d, want 1", exhausted.Slots)
	}
}

func TestClaimSlotEmptyPoolIsAnError(t *testing.T) {
	if slot, err := claimSlotIn(t.TempDir(), "  \n "); slot != nil || err == nil {
		t.Fatalf("empty pool: slot=%v err=%v, want nil slot and an error", slot, err)
	}
}

func TestClaimSlotExportedUsesPoolLockDir(t *testing.T) {
	tmp := t.TempDir()
	// os.TempDir consults these, so the exported path lands in the temp dir.
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)

	slot, err := ClaimSlot("live-probe")
	if err != nil {
		t.Fatalf("ClaimSlot: %v", err)
	}
	if slot.Index != 0 || slot.Session != "live-probe" {
		t.Fatalf("claimed slot %d/%q, want slot 0/live-probe", slot.Index, slot.Session)
	}
	if want := PoolLockDir() + string(filepath.Separator); !strings.HasPrefix(slot.Lock.Path(), want) {
		t.Fatalf("claimed lock %q is not under %q", slot.Lock.Path(), want)
	}
}
