package modbusmanager

import (
	"testing"
	"time"
)

func TestSpanCoversAndOverlaps(t *testing.T) {
	block := span{unitId: 1, kind: holdingRegisters, addr: 100, qty: 10} // 100-109
	for _, tc := range []struct {
		name             string
		other            span
		covers, overlaps bool
	}{
		{"same", span{1, holdingRegisters, 100, 10}, true, true},
		{"inside", span{1, holdingRegisters, 102, 3}, true, true},
		{"last address", span{1, holdingRegisters, 109, 1}, true, true},
		{"past the end", span{1, holdingRegisters, 109, 2}, false, true},
		{"next address", span{1, holdingRegisters, 110, 1}, false, false},
		{"before the start", span{1, holdingRegisters, 99, 2}, false, true},
		{"just before", span{1, holdingRegisters, 99, 1}, false, false},
		{"around", span{1, holdingRegisters, 90, 30}, false, true},
		{"other unit", span{2, holdingRegisters, 100, 10}, false, false},
		{"other table", span{1, inputRegisters, 100, 10}, false, false},
	} {
		if got := block.covers(tc.other); got != tc.covers {
			t.Errorf("%s: covers = %v, want %v", tc.name, got, tc.covers)
		}
		if got := block.overlaps(tc.other); got != tc.overlaps {
			t.Errorf("%s: overlaps = %v, want %v", tc.name, got, tc.overlaps)
		}
	}
}

func TestCacheKeepsAtMostMaxEntries(t *testing.T) {
	var c cache
	expires := time.Now().Add(time.Hour)
	for i := range maxCacheEntries + 1 {
		c.put(span{unitId: 1, kind: coils, addr: uint16(i), qty: 1}, []bool{true}, expires)
	}

	if len(c.entries) != maxCacheEntries {
		t.Errorf("%d entries, want %d", len(c.entries), maxCacheEntries)
	}
	if _, ok := c.get(span{unitId: 1, kind: coils, addr: 0, qty: 1}, time.Now()); ok {
		t.Error("the oldest entry is still there")
	}
	if _, ok := c.get(span{unitId: 1, kind: coils, addr: maxCacheEntries, qty: 1}, time.Now()); !ok {
		t.Error("the newest entry is missing")
	}
}
