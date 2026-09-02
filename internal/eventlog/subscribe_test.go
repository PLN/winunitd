package eventlog

import (
	"errors"
	"strings"
	"testing"
)

func TestSubscribeQueryIsEventIDXPath(t *testing.T) {
	t.Parallel()
	tr, err := ParseTrigger("Application:EventID=1234")
	if err != nil {
		t.Fatal(err)
	}
	got, q, err := subscribeQuery(tr)
	if err != nil {
		t.Fatal(err)
	}
	if got.Channel != "Application" || got.EventID != 1234 {
		t.Fatalf("resolved = %+v", got)
	}
	if q != "*[System[(EventID=1234)]]" {
		t.Fatalf("query = %q", q)
	}

	fromRaw, q2, err := subscribeQuery(Trigger{Raw: "System:EventID=9"})
	if err != nil {
		t.Fatal(err)
	}
	if fromRaw.Channel != "System" || fromRaw.EventID != 9 || q2 != "*[System[(EventID=9)]]" {
		t.Fatalf("from raw: %+v q=%q", fromRaw, q2)
	}
}

func TestSubscribeQueryRejectsNullAndEmpty(t *testing.T) {
	t.Parallel()
	cases := []Trigger{
		{},
		{Channel: "Application"},
		{EventID: 0},
		{Channel: "Application", EventID: 0},
		{Raw: ""},
		{Raw: "   "},
	}
	for _, tr := range cases {
		_, q, err := subscribeQuery(tr)
		if err == nil || q != "" {
			t.Fatalf("empty query must error: tr=%+v q=%q err=%v", tr, q, err)
		}
		if errors.Is(err, ErrEmptyQuery) {
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "empty") && !strings.Contains(msg, "EventLogTrigger") && !strings.Contains(msg, "EventID") {
			t.Fatalf("empty query err = %v", err)
		}
	}
}

func TestOpenSubscribeRejectsEmptyQuery(t *testing.T) {
	t.Parallel()
	cases := []Trigger{
		{},
		{Channel: "Application"},
		{EventID: 0, Raw: ""},
		{Channel: "Application", EventID: 0},
	}
	for _, tr := range cases {
		s, err := OpenSubscribe(tr)
		if s != nil {
			_ = s.Close()
			t.Fatalf("empty query must not subscribe: %+v", tr)
		}
		if err == nil {
			t.Fatalf("empty query must error: %+v", tr)
		}
		if strings.Contains(err.Error(), "not supported") {
			t.Fatalf("empty query must fail before platform stub: %v", err)
		}
		if errors.Is(err, ErrEmptyQuery) {
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "empty") && !strings.Contains(msg, "EventLogTrigger") && !strings.Contains(msg, "EventID") {
			t.Fatalf("empty query err = %v", err)
		}
	}
}
