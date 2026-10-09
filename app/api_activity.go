package app

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/womat/golib/web"
	"github.com/womat/modbusgateway/pkg/activity"
)

// ActivityResponse is who talks to the gateway: its listeners, the clients of the last
// ten minutes, the buses and the last transactions. It holds no register values.
type ActivityResponse struct {
	Listeners []ListenerActivity    `json:"listeners"`
	Clients   []ClientActivity      `json:"clients"` // the most recent first
	Buses     []BusActivity         `json:"buses"`
	Recent    []TransactionActivity `json:"recent"` // the last 100, newest first
}

// ListenerActivity is one entry of the gateway: the REST API or a Modbus listener.
type ListenerActivity struct {
	Source          string `json:"source"`                    // rest | tcp | rtu
	Address         string `json:"address"`                   // host:port, or the serial port
	Settings        string `json:"settings,omitempty"`        // rtu: line settings, e.g. 9600 8N1
	OpenConnections *int   `json:"openConnections,omitempty"` // tcp: open connections, silent ones included
}

// ClientActivity sums up one client: a REST caller or a Modbus TCP connection by IP address, or
// the client on the serial line of the RTU listener (empty address).
type ClientActivity struct {
	Source         string           `json:"source"` // rest | tcp | rtu
	Address        string           `json:"address,omitempty"`
	FirstAt        string           `json:"firstAt"`
	LastAt         string           `json:"lastAt"`
	Requests       uint64           `json:"requests"`
	Errors         uint64           `json:"errors"`    // timeouts, exceptions and refused requests
	PerMinute      int              `json:"perMinute"` // requests in the last 60 s
	Targets        []TargetActivity `json:"targets"`
	Connected      *bool            `json:"connected,omitempty"`      // tcp: a connection is open
	ConnectedSince string           `json:"connectedSince,omitempty"` // tcp: the oldest open connection
}

// TargetActivity is a device a client asked, with the unit ID it used on a Modbus listener.
type TargetActivity struct {
	Device string `json:"device"`
	UnitId uint8  `json:"unitId,omitempty"`
}

// BusActivity is one bus: its connection, its queue and the transactions that reached it.
type BusActivity struct {
	Name          string  `json:"name"`
	Type          string  `json:"type"`    // tcp | rtu
	Address       string  `json:"address"` // host:port, or the serial port with its line settings
	Connected     bool    `json:"connected"`
	LastConnectAt string  `json:"lastConnectAt,omitempty"`
	QueueLen      int     `json:"queueLen"`
	QueueSize     int     `json:"queueSize"`
	Transactions  uint64  `json:"transactions"` // since the start
	Errors        uint64  `json:"errors"`
	Timeouts      uint64  `json:"timeouts"`
	MeanDuration  float64 `json:"meanDuration"` // seconds per transaction
}

// TransactionActivity is one request: who asked whom for what, and what came of it.
type TransactionActivity struct {
	Time     string  `json:"time"`
	Source   string  `json:"source"`           // rest | tcp | rtu
	Client   string  `json:"client,omitempty"` // IP address; empty for the serial line
	Device   string  `json:"device"`
	UnitId   uint8   `json:"unitId,omitempty"` // the unit ID used on a Modbus listener
	Function uint8   `json:"functionCode"`
	Address  uint16  `json:"address"`
	Quantity uint16  `json:"quantity"`
	Result   string  `json:"result"` // ok, cache, timeout, forbidden, the exception of the device, ...
	Class    string  `json:"class"`  // ok | cache | exception | timeout | error | rejected
	Duration float64 `json:"duration"`
}

// HandleActivity returns who talks to the gateway.
//
//	@Summary		Get the communication activity
//	@Description	Returns the listeners, the clients of the last 10 min (REST callers of /devices/…, Modbus TCP connections, the Modbus RTU line), the buses and the last 100 transactions. No register values.
//	@Tags			info
//	@Produce		json
//	@Security		ApiKeyAuth
//	@Success		200	{object}	ActivityResponse	"Activity"
//	@Failure		401	{string}	string				"Unauthorized"
//	@Router			/activity [get]
func (app *App) HandleActivity() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		web.Encode(w, http.StatusOK, app.activitySnapshot())
	})
}

func (app *App) activitySnapshot() ActivityResponse {
	resp := ActivityResponse{
		Listeners: []ListenerActivity{{
			Source:  string(activity.SourceREST),
			Address: net.JoinHostPort(app.config.Webserver.ListenHost, strconv.Itoa(app.config.Webserver.ListenPort)),
		}},
		Clients: []ClientActivity{},
		Buses:   []BusActivity{},
		Recent:  []TransactionActivity{},
	}

	// The open Modbus TCP connections, by client address, the oldest first.
	open := map[string]time.Time{}
	for _, server := range app.modbusServers {
		l := server.Listener()
		switch {
		case l.TCP != nil:
			conns := server.Connections()
			n := len(conns)
			resp.Listeners = append(resp.Listeners, ListenerActivity{
				Source: string(activity.SourceTCP), Address: net.JoinHostPort(l.TCP.Host, strconv.Itoa(l.TCP.Port)), OpenConnections: &n,
			})
			for _, c := range conns {
				if since, ok := open[c.Remote]; !ok || c.Since.Before(since) {
					open[c.Remote] = c.Since
				}
			}
		case l.RTU != nil:
			resp.Listeners = append(resp.Listeners, ListenerActivity{
				Source: string(activity.SourceRTU), Address: l.RTU.Port,
				Settings: strconv.Itoa(l.RTU.BaudRate) + " " + strconv.Itoa(l.RTU.DataBits) + l.RTU.Parity + strconv.Itoa(l.RTU.StopBits),
			})
		}
	}

	for _, c := range app.activity.Clients() {
		out := ClientActivity{
			Source: string(c.Source), Address: c.Address, FirstAt: formatTime(c.FirstAt), LastAt: formatTime(c.LastAt),
			Requests: c.Requests, Errors: c.Errors, PerMinute: c.PerMinute, Targets: make([]TargetActivity, 0, len(c.Targets)),
		}
		for _, t := range c.Targets {
			out.Targets = append(out.Targets, TargetActivity{Device: t.Device, UnitId: t.UnitId})
		}
		if c.Source == activity.SourceTCP {
			since, connected := open[c.Address]
			out.Connected = &connected
			if connected {
				out.ConnectedSince = formatTime(since)
			}
		}
		resp.Clients = append(resp.Clients, out)
	}

	if app.modbusMgr != nil {
		for _, b := range app.modbusMgr.BusStatus() {
			out := BusActivity{
				Name: b.Name, Type: b.Type, Address: b.Address, Connected: b.Connected, LastConnectAt: formatTime(b.LastConnectAt),
				QueueLen: b.QueueLen, QueueSize: b.QueueSize, Transactions: b.Transactions, Errors: b.Errors, Timeouts: b.Timeouts,
			}
			if b.Transactions > 0 {
				out.MeanDuration = b.BusyTime.Seconds() / float64(b.Transactions)
			}
			resp.Buses = append(resp.Buses, out)
		}
	}

	for _, t := range app.activity.Recent() {
		resp.Recent = append(resp.Recent, TransactionActivity{
			Time: t.Time.UTC().Format(time.RFC3339Nano), Source: string(t.Source), Client: t.Client, Device: t.Device,
			UnitId: t.UnitId, Function: t.Function, Address: t.Address, Quantity: t.Quantity,
			Result: t.Result, Class: t.Class, Duration: t.Duration.Seconds(),
		})
	}
	return resp
}

// formatTime is t in UTC as RFC 3339, or empty for the zero time.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
