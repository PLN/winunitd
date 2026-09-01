//go:build !windows

package eventlog

import (
	"strings"
	"testing"
)

func TestOpenSubscribeStub(t *testing.T) {
	t.Parallel()
	tr, err := ParseTrigger("Application:EventID=1234")
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenSubscribe(tr)
	if err == nil || s != nil {
		t.Fatalf("stub must not subscribe: s=%v err=%v", s, err)
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err = %v", err)
	}
}
