// Package activity records who talks to the gateway: the last transactions and, per client, how
// often and with which result it asked. It keeps no values, only the request and its outcome.
//
// A client is a REST caller or a Modbus TCP connection by its IP address, or the client on the
// serial line of the Modbus RTU listener. A client is forgotten ClientIdle after its last
// request; at most MaxClients are kept, the one silent the longest goes first.
package activity

import (
	"cmp"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// Size is the number of transactions kept, newest first.
	Size = 100
	// MaxClients bounds the client list, e.g. against a scanner with changing addresses.
	MaxClients = 32
	// ClientIdle is how long a client stays listed after its last request.
	ClientIdle = 10 * time.Minute
)

// Source is the entry a request came through.
type Source string

const (
	SourceREST Source = "rest" // the REST API
	SourceTCP  Source = "tcp"  // the Modbus TCP listener
	SourceRTU  Source = "rtu"  // the Modbus RTU listener
)

// Transaction is one request: who asked whom for what, and what came of it. No values.
type Transaction struct {
	Time     time.Time
	Source   Source
	Client   string // IP address; empty for the serial line of the RTU listener
	Device   string
	UnitId   uint8 // the unit ID the client used on a Modbus listener; 0 for REST
	Function uint8
	Address  uint16
	Quantity uint16
	Result   string // "ok", "cache", "timeout", the exception of the device, ...
	Class    string // ok | cache | exception | timeout | error | rejected
	Duration time.Duration
}

// failed reports whether the transaction counts as an error of its client.
func (t Transaction) failed() bool {
	return t.Class != "ok" && t.Class != "cache"
}

// Client sums up the requests of one client.
type Client struct {
	Source    Source
	Address   string    // IP address; empty for the serial line of the RTU listener
	FirstAt   time.Time // first request since the client was listed
	LastAt    time.Time
	Requests  uint64
	Errors    uint64
	PerMinute int      // requests in the last 60 s
	Targets   []Target // the devices it asked, by name
}

// Target is a device a client asked, with the unit ID it used on a Modbus listener.
type Target struct {
	Device string
	UnitId uint8
}

type clientKey struct {
	source  Source
	address string
}

type client struct {
	Client
	seconds [60]struct {
		at int64 // unix second
		n  int
	}
	targets map[Target]bool
}

// Recorder keeps the transactions and the clients; it is safe for concurrent use, and a nil
// Recorder records nothing.
type Recorder struct {
	mu      sync.Mutex
	now     func() time.Time
	ring    [Size]Transaction
	next    int
	count   int
	clients map[clientKey]*client
}

// New returns an empty Recorder.
func New() *Recorder {
	return &Recorder{now: time.Now, clients: map[clientKey]*client{}}
}

// Record adds a transaction; a zero Time is set to now.
func (r *Recorder) Record(t Transaction) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if t.Time.IsZero() {
		t.Time = r.now()
	}
	r.ring[r.next] = t
	r.next = (r.next + 1) % Size
	r.count = min(r.count+1, Size)

	r.expire(t.Time)
	key := clientKey{t.Source, t.Client}
	c := r.clients[key]
	if c == nil {
		if len(r.clients) >= MaxClients {
			r.evictQuietest()
		}
		c = &client{Client: Client{Source: t.Source, Address: t.Client, FirstAt: t.Time}, targets: map[Target]bool{}}
		r.clients[key] = c
	}
	c.LastAt = t.Time
	c.Requests++
	if t.failed() {
		c.Errors++
	}
	slot := &c.seconds[t.Time.Unix()%60]
	if slot.at != t.Time.Unix() {
		slot.at, slot.n = t.Time.Unix(), 0
	}
	slot.n++
	c.targets[Target{Device: t.Device, UnitId: t.UnitId}] = true
}

// Recent returns the kept transactions, newest first.
func (r *Recorder) Recent() []Transaction {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	list := make([]Transaction, 0, r.count)
	for i := 1; i <= r.count; i++ {
		list = append(list, r.ring[(r.next-i+Size)%Size])
	}
	return list
}

// Clients returns the clients with a request in the last ClientIdle, the most recent first.
func (r *Recorder) Clients() []Client {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.expire(now)

	list := make([]Client, 0, len(r.clients))
	for _, c := range r.clients {
		out := c.Client
		for _, s := range c.seconds {
			if s.at > now.Unix()-60 {
				out.PerMinute += s.n
			}
		}
		for t := range c.targets {
			out.Targets = append(out.Targets, t)
		}
		slices.SortFunc(out.Targets, func(a, b Target) int {
			return cmp.Or(strings.Compare(a.Device, b.Device), cmp.Compare(a.UnitId, b.UnitId))
		})
		list = append(list, out)
	}
	slices.SortFunc(list, func(a, b Client) int { return b.LastAt.Compare(a.LastAt) })
	return list
}

// expire forgets the clients silent for longer than ClientIdle.
func (r *Recorder) expire(now time.Time) {
	for key, c := range r.clients {
		if now.Sub(c.LastAt) > ClientIdle {
			delete(r.clients, key)
		}
	}
}

// evictQuietest forgets the client whose last request is the oldest.
func (r *Recorder) evictQuietest() {
	var oldest clientKey
	var at time.Time
	for key, c := range r.clients {
		if at.IsZero() || c.LastAt.Before(at) {
			oldest, at = key, c.LastAt
		}
	}
	delete(r.clients, oldest)
}
