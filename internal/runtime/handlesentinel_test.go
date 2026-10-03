package runtime

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// Handle-sentinel classes beyond files. Each sentinel is a named kernel
// object whose name carries a nonce; a section also holds a second nonce in
// its contents. A child is told only the kinds and handle numbers; it reports
// the identity of whatever object each number refers to in its own handle
// table, and the parent decides which sentinels it really inherited.
const (
	SentinelEvent   = "event"
	SentinelSection = "section"
)

// sentinelTypes is the NT object type each sentinel kind must have.
var sentinelTypes = map[string]string{SentinelEvent: "Event", SentinelSection: "Section"}

// SentinelName is the object name of a sentinel of a kind with a nonce.
func SentinelName(kind, nonce string) string { return "winunitd-r4-" + kind + "-" + nonce }

// What a probe established about the object a handle number names.
const (
	// ObjectAbsent: the number names no handle.
	ObjectAbsent = "absent"
	// ObjectOtherType: a handle to an object of another type.
	ObjectOtherType = "other-type"
	// ObjectIdentified: an object of the expected type whose name, and for
	// a section contents, were read.
	ObjectIdentified = "identified"
	// ObjectUnknown: a query failed, so nothing is established.
	ObjectUnknown = "unknown"
)

// ObjectIdentity is the identity of the object a handle refers to: the
// sentinel kind asked about, what the probe established, the NT object
// type, the full object name and, for a section, the nonce in its first
// bytes. Error names the failed query of an unknown identity.
type ObjectIdentity struct {
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Type    string `json:"type,omitempty"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

// complete reports whether an identity fully identifies a sentinel: a known
// kind, identified with its object type, a name and, for a section,
// content.
func (o ObjectIdentity) complete() bool {
	want, ok := sentinelTypes[o.Kind]
	return ok && o.Status == ObjectIdentified && o.Type == want && o.Name != "" && (o.Kind != SentinelSection || o.Content != "") && o.Error == ""
}

// ObjectSentinelRef is one sentinel a child is asked about.
type ObjectSentinelRef struct {
	Kind   string
	Handle uint64
}

// FormatObjectSentinels is the child argument value for a list of sentinels.
func FormatObjectSentinels(refs []ObjectSentinelRef) string {
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		parts = append(parts, r.Kind+":"+strconv.FormatUint(r.Handle, 10))
	}
	return strings.Join(parts, ",")
}

// ParseObjectSentinels reads kind:handle,... and refuses unknown kinds,
// malformed or zero handles and an empty list.
func ParseObjectSentinels(value string) ([]ObjectSentinelRef, error) {
	if value == "" {
		return nil, fmt.Errorf("no object sentinels")
	}
	var refs []ObjectSentinelRef
	for _, part := range strings.Split(value, ",") {
		kind, number, ok := strings.Cut(part, ":")
		if _, known := sentinelTypes[kind]; !ok || !known {
			return nil, fmt.Errorf("object sentinel %q", part)
		}
		h, err := strconv.ParseUint(number, 10, 64)
		if err != nil || h == 0 {
			return nil, fmt.Errorf("object sentinel %q", part)
		}
		refs = append(refs, ObjectSentinelRef{Kind: kind, Handle: h})
	}
	return refs, nil
}

// InheritedSentinels decides, per sentinel, whether the child's observation
// at that sentinel's handle number is that sentinel: identified with the
// same kind, object type, full name and content. A number that names no
// handle, an object of another type, or an identified object with another
// name or contents is not the sentinel. An unknown or contradictory
// observation establishes nothing, so it is an error, never absence.
func InheritedSentinels(sent, observed []ObjectIdentity) ([]bool, error) {
	if len(observed) != len(sent) {
		return nil, fmt.Errorf("%d observations for %d sentinels", len(observed), len(sent))
	}
	out := make([]bool, len(sent))
	for i, s := range sent {
		if !s.complete() {
			return nil, fmt.Errorf("sentinel %d has no complete identity", i)
		}
		o := observed[i]
		if o.Kind != s.Kind {
			return nil, fmt.Errorf("observation %d is for another kind", i)
		}
		switch {
		case o.Status == ObjectAbsent && o.Type == "" && o.Name == "" && o.Content == "" && o.Error == "":
		case o.Status == ObjectOtherType && o.Type != "" && o.Type != s.Type && o.Name == "" && o.Content == "" && o.Error == "":
		case o.Status == ObjectIdentified && o.Type == s.Type && o.Error == "" && (o.Kind != SentinelSection || o.Content != ""):
			out[i] = o == s
		default:
			return nil, fmt.Errorf("observation %d establishes nothing (%s %s)", i, o.Status, o.Error)
		}
	}
	return out, nil
}

func TestParseObjectSentinels(t *testing.T) {
	refs := []ObjectSentinelRef{{SentinelEvent, 412}, {SentinelSection, 9000}}
	got, err := ParseObjectSentinels(FormatObjectSentinels(refs))
	if err != nil || len(got) != 2 || got[0] != refs[0] || got[1] != refs[1] {
		t.Fatalf("round trip %v %v", got, err)
	}
	for _, bad := range []string{"", "event", "event:", "event:0", "event:-4", "mutex:12", "event:12,", "file:12", "event:0x10"} {
		if _, err := ParseObjectSentinels(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestInheritedSentinels(t *testing.T) {
	const id = ObjectIdentified
	event := ObjectIdentity{Kind: SentinelEvent, Status: id, Type: "Event", Name: `\BaseNamedObjects\` + SentinelName(SentinelEvent, "n1")}
	section := ObjectIdentity{Kind: SentinelSection, Status: id, Type: "Section", Name: `\BaseNamedObjects\` + SentinelName(SentinelSection, "n2"), Content: "c2"}
	sent := []ObjectIdentity{event, section}
	got, err := InheritedSentinels(sent, []ObjectIdentity{event, section})
	if err != nil || !got[0] || !got[1] {
		t.Fatalf("exact identities %v %v", got, err)
	}
	// Each observation that establishes another object, or none, at a
	// sentinel's number is not that sentinel; the other decision stands.
	other := `\Sessions\2\BaseNamedObjects\` + SentinelName(SentinelSection, "n2")
	for name, c := range map[string]struct {
		observed [2]ObjectIdentity
		want     [2]bool
	}{
		"no handle at the numbers": {[2]ObjectIdentity{{Kind: SentinelEvent, Status: ObjectAbsent}, {Kind: SentinelSection, Status: ObjectAbsent}}, [2]bool{}},
		"another event reusing a number": {[2]ObjectIdentity{{Kind: SentinelEvent, Status: id, Type: "Event", Name: `\BaseNamedObjects\other`}, section},
			[2]bool{false, true}},
		"an unnamed event":             {[2]ObjectIdentity{{Kind: SentinelEvent, Status: id, Type: "Event"}, section}, [2]bool{false, true}},
		"a file at the event's number": {[2]ObjectIdentity{{Kind: SentinelEvent, Status: ObjectOtherType, Type: "File"}, section}, [2]bool{false, true}},
		"a section with other contents": {[2]ObjectIdentity{event, {Kind: SentinelSection, Status: id, Type: "Section", Name: section.Name, Content: "other"}},
			[2]bool{true, false}},
		"a section in another namespace": {[2]ObjectIdentity{event, {Kind: SentinelSection, Status: id, Type: "Section", Name: other, Content: "c2"}},
			[2]bool{true, false}},
	} {
		got, err := InheritedSentinels(sent, c.observed[:])
		if err != nil || got[0] != c.want[0] || got[1] != c.want[1] {
			t.Errorf("%s: decisions %v %v, want %v", name, got, err, c.want)
		}
	}
	// An unknown or contradictory observation establishes nothing: it is an
	// error, never evidence that the sentinel is absent.
	for name, o := range map[string]ObjectIdentity{
		"failed type query":                {Kind: SentinelEvent, Status: ObjectUnknown, Error: "type 0xc0000022"},
		"failed name query":                {Kind: SentinelEvent, Status: ObjectUnknown, Type: "Event", Error: "name 0xc0000022"},
		"failed handle duplication":        {Kind: SentinelEvent, Status: ObjectUnknown, Error: "duplicate 5"},
		"no status":                        {Kind: SentinelEvent, Type: "Event", Name: event.Name},
		"identified with an error":         {Kind: SentinelEvent, Status: id, Type: "Event", Name: event.Name, Error: "name 0x80000005"},
		"identified as another type":       {Kind: SentinelEvent, Status: id, Type: "Mutant", Name: event.Name},
		"other type naming the right type": {Kind: SentinelEvent, Status: ObjectOtherType, Type: "Event"},
		"other type with a name":           {Kind: SentinelEvent, Status: ObjectOtherType, Type: "Mutant", Name: event.Name},
		"absent with a type":               {Kind: SentinelEvent, Status: ObjectAbsent, Type: "Event"},
	} {
		if _, err := InheritedSentinels(sent[:1], []ObjectIdentity{o}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for name, o := range map[string]ObjectIdentity{
		"a section whose view failed":       {Kind: SentinelSection, Status: ObjectUnknown, Type: "Section", Name: section.Name, Error: "view 5"},
		"a section identified without view": {Kind: SentinelSection, Status: id, Type: "Section", Name: section.Name},
	} {
		if _, err := InheritedSentinels(sent[1:], []ObjectIdentity{o}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for name, c := range map[string]struct{ sent, observed []ObjectIdentity }{
		"missing observation":         {sent, []ObjectIdentity{event}},
		"extra observation":           {sent, []ObjectIdentity{event, section, event}},
		"observation of another kind": {sent, []ObjectIdentity{section, event}},
		"sentinel without a name":     {[]ObjectIdentity{{Kind: SentinelEvent, Status: id, Type: "Event"}}, []ObjectIdentity{{Kind: SentinelEvent, Status: id, Type: "Event"}}},
		"section without content": {[]ObjectIdentity{{Kind: SentinelSection, Status: id, Type: "Section", Name: "s"}},
			[]ObjectIdentity{{Kind: SentinelSection, Status: id, Type: "Section", Name: "s"}}},
		"sentinel of a wrong type": {[]ObjectIdentity{{Kind: SentinelEvent, Status: id, Type: "Mutant", Name: "e"}},
			[]ObjectIdentity{{Kind: SentinelEvent, Status: id, Type: "Mutant", Name: "e"}}},
		"sentinel not identified": {[]ObjectIdentity{{Kind: SentinelEvent, Status: ObjectUnknown, Type: "Event", Name: "e"}},
			[]ObjectIdentity{{Kind: SentinelEvent, Status: ObjectAbsent}}},
		"unknown kind":     {[]ObjectIdentity{{Kind: "mutex", Status: id, Type: "Mutant", Name: "m"}}, []ObjectIdentity{{Kind: "mutex", Status: id, Type: "Mutant", Name: "m"}}},
		"empty identities": {[]ObjectIdentity{{}}, []ObjectIdentity{{}}},
	} {
		if _, err := InheritedSentinels(c.sent, c.observed); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
