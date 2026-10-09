package modbusmanager

import (
	"slices"
	"time"
)

// regType is the Modbus data table a request addresses.
type regType uint8

const (
	coils regType = iota
	discreteInputs
	holdingRegisters
	inputRegisters
)

// span is a range of one data table of one unit ID; it is the cache key of a read and the
// range a write changes.
type span struct {
	unitId uint8
	kind   regType
	addr   uint16
	qty    uint16
}

// covers reports whether s contains all of other.
func (s span) covers(other span) bool {
	return s.unitId == other.unitId && s.kind == other.kind &&
		other.addr >= s.addr && int(other.addr)+int(other.qty) <= int(s.addr)+int(s.qty)
}

// overlaps reports whether s and other share at least one address.
func (s span) overlaps(other span) bool {
	return s.unitId == other.unitId && s.kind == other.kind &&
		int(other.addr) < int(s.addr)+int(s.qty) && int(s.addr) < int(other.addr)+int(other.qty)
}

// maxCacheEntries bounds the memory of a bus cache; the oldest entry goes first.
const maxCacheEntries = 256

// cache holds recent read results of one bus. Bits are stored as []bool with one value per
// address, registers as raw bytes with two per address. It is guarded by bus.mu.
type cache struct {
	entries []cacheEntry
}

type cacheEntry struct {
	span    span
	value   any // []bool or []byte
	expires time.Time
}

// get returns a copy of the values for key when an entry that is still valid covers it.
func (c *cache) get(key span, now time.Time) (any, bool) {
	for _, e := range c.entries {
		if !now.Before(e.expires) || !e.span.covers(key) {
			continue
		}
		offset := int(key.addr - e.span.addr)
		switch v := e.value.(type) {
		case []bool:
			return slices.Clone(v[offset : offset+int(key.qty)]), true
		case []byte:
			return slices.Clone(v[offset*2 : (offset+int(key.qty))*2]), true
		}
	}
	return nil, false
}

// put stores value for key until expires, replacing an entry for the same span and dropping
// expired ones.
func (c *cache) put(key span, value any, expires time.Time) {
	now := time.Now()
	c.entries = slices.DeleteFunc(c.entries, func(e cacheEntry) bool {
		return e.span == key || !now.Before(e.expires)
	})
	if len(c.entries) >= maxCacheEntries {
		c.entries = c.entries[1:]
	}
	c.entries = append(c.entries, cacheEntry{span: key, value: value, expires: expires})
}

// invalidate drops every entry that shares an address with written.
func (c *cache) invalidate(written span) {
	c.entries = slices.DeleteFunc(c.entries, func(e cacheEntry) bool {
		return e.span.overlaps(written)
	})
}
