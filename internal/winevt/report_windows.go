//go:build windows

package winevt

import "golang.org/x/sys/windows/svc/eventlog"

func report(id uint32, kind Kind, insert string) {
	if id == 0 {
		return
	}
	log, err := eventlog.Open(Source)
	if err != nil {
		return
	}
	defer log.Close()
	switch kind {
	case KindInfo:
		_ = log.Info(id, insert)
	case KindWarning:
		_ = log.Warning(id, insert)
	default:
		_ = log.Error(id, insert)
	}
}
