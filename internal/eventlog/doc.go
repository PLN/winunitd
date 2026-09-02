// Package eventlog parses EventLogTrigger= values and subscribes to
// Windows Event Log channels.
//
// Grammar is <Channel>:EventID=<uint16> only (DESIGN.md §6.7 / §39).
// Channel is a literal log name. There is no XPath, Provider=, or Level=
// in the unit file. Subscriptions use EvtSubscribe (push) with the
// EventID XPath *[System[(EventID=N)]] on the wire. A null or empty
// query is a subscribe error (not match-all). Linux builds stub
// OpenSubscribe so parse/verify and protocol tests stay portable.
package eventlog
