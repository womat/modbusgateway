package activity

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// clock is a settable time for the Recorder.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestRecorder() (*Recorder, *clock) {
	c := &clock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	r := New()
	r.now = c.now
	return r, c
}

func TestRecentKeepsTheLastTransactionsNewestFirst(t *testing.T) {
	r, c := newTestRecorder()
	for i := range Size + 5 {
		r.Record(Transaction{Time: c.t, Source: SourceREST, Client: "10.0.0.1", Address: uint16(i), Class: "ok"})
	}
	recent := r.Recent()
	if len(recent) != Size {
		t.Fatalf("len(Recent()) = %d, want %d", len(recent), Size)
	}
	if recent[0].Address != Size+4 || recent[Size-1].Address != 5 {
		t.Errorf("Recent() runs from address %d to %d, want %d to 5", recent[0].Address, recent[Size-1].Address, Size+4)
	}
}

func TestClientsCountRequestsErrorsAndTargets(t *testing.T) {
	r, c := newTestRecorder()
	r.Record(Transaction{Time: c.t, Source: SourceTCP, Client: "10.0.0.2", Device: "meter", UnitId: 11, Class: "ok"})
	r.Record(Transaction{Time: c.t, Source: SourceTCP, Client: "10.0.0.2", Device: "meter", UnitId: 11, Class: "cache"})
	r.Record(Transaction{Time: c.t, Source: SourceTCP, Client: "10.0.0.2", Device: "pump", UnitId: 12, Class: "timeout"})
	c.t = c.t.Add(90 * time.Second)
	r.Record(Transaction{Time: c.t, Source: SourceREST, Client: "10.0.0.2", Device: "meter", Class: "rejected"})

	clients := r.Clients()
	if len(clients) != 2 {
		t.Fatalf("Clients() = %+v, want REST and TCP of 10.0.0.2 as two clients", clients)
	}
	rest, tcp := clients[0], clients[1]
	if rest.Source != SourceREST || rest.Requests != 1 || rest.Errors != 1 || rest.PerMinute != 1 {
		t.Errorf("REST client = %+v, want 1 request, 1 error, 1 in the last minute", rest)
	}
	if tcp.Requests != 3 || tcp.Errors != 1 || tcp.PerMinute != 0 {
		t.Errorf("TCP client = %+v, want 3 requests, 1 error, none in the last minute", tcp)
	}
	want := []Target{{"meter", 11}, {"pump", 12}}
	if !slices.Equal(tcp.Targets, want) {
		t.Errorf("TCP targets = %v, want %v", tcp.Targets, want)
	}
}

func TestClientsExpireAfterTheIdleTime(t *testing.T) {
	r, c := newTestRecorder()
	r.Record(Transaction{Time: c.t, Source: SourceREST, Client: "10.0.0.3", Class: "ok"})
	c.t = c.t.Add(ClientIdle)
	if len(r.Clients()) != 1 {
		t.Fatal("client gone at exactly ClientIdle, want it still listed")
	}
	c.t = c.t.Add(time.Second)
	if clients := r.Clients(); len(clients) != 0 {
		t.Errorf("Clients() = %+v after ClientIdle, want none", clients)
	}
}

func TestClientsAreBounded(t *testing.T) {
	r, c := newTestRecorder()
	for i := range MaxClients + 3 {
		c.t = c.t.Add(time.Second)
		r.Record(Transaction{Time: c.t, Source: SourceREST, Client: fmt.Sprintf("10.0.1.%d", i), Class: "ok"})
	}
	clients := r.Clients()
	if len(clients) != MaxClients {
		t.Fatalf("len(Clients()) = %d, want %d", len(clients), MaxClients)
	}
	for _, cl := range clients {
		if cl.Address == "10.0.1.0" || cl.Address == "10.0.1.2" {
			t.Errorf("client %s is still listed, want the quietest ones evicted", cl.Address)
		}
	}
}

func TestRecordConcurrently(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 200 {
				r.Record(Transaction{Source: SourceTCP, Client: fmt.Sprintf("10.0.2.%d", i), Class: "ok"})
				_ = r.Recent()
				_ = r.Clients()
			}
		})
	}
	wg.Wait()
	total := uint64(0)
	for _, c := range r.Clients() {
		total += c.Requests
	}
	if total != 8*200 {
		t.Errorf("requests counted = %d, want %d", total, 8*200)
	}
}

func TestNilRecorderRecordsNothing(t *testing.T) {
	var r *Recorder
	r.Record(Transaction{})
	if r.Recent() != nil || r.Clients() != nil {
		t.Error("a nil Recorder returned data")
	}
}
