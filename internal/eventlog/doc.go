// Package eventlog parses EventLogTrigger= values and subscribes to
// Windows Event Log channels.
//
// Grammar is <Channel>:EventID=<uint16> only (DESIGN.md §39). Channel is
// a literal log name. There is no XPath, Provider=, or Level= in the
// unit file. Subscriptions use EvtSubscribe (push). Linux builds stub
// OpenSubscribe so parse/verify and protocol tests stay portable.
package eventlog
