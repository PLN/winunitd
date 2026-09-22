//go:build !windows

package winevt

func report(uint32, Kind, string) {}
