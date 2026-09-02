package eventlog

import (
	"strconv"
	"strings"
)

// eventIDFromXML reads the EventID element from EvtRender XML.
// Classic events may be <EventID Qualifiers="...">1234</EventID>.
// Subscribe matching does not use this; EvtSubscribe applies Query.
func eventIDFromXML(xml string) (uint16, bool) {
	i := strings.Index(xml, "<EventID")
	if i < 0 {
		return 0, false
	}
	rest := xml[i:]
	j := strings.IndexByte(rest, '>')
	if j < 0 || j+1 >= len(rest) {
		return 0, false
	}
	rest = rest[j+1:]
	k := strings.IndexByte(rest, '<')
	if k < 0 {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(rest[:k]), 10, 16)
	if err != nil || n == 0 {
		return 0, false
	}
	return uint16(n), true
}
