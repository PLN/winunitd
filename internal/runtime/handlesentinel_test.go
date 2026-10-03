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

// ObjectIdentity is the identity of the object a handle refers to: the
// sentinel kind asked about, the NT object type, the full object name and,
// for a section, the nonce in its first bytes. Only the kind is set when the
// handle refers to no object of the expected type.
type ObjectIdentity struct {
	Kind    string `json:"kind"`
	Type    string `json:"type,omitempty"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content,omitempty"`
}

// complete reports whether an identity fully identifies a sentinel: a known
// kind, its object type, a name and, for a section, content.
func (o ObjectIdentity) complete() bool {
	want, ok := sentinelTypes[o.Kind]
	return ok && o.Type == want && o.Name != "" && (o.Kind != SentinelSection || o.Content != "")
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
// at that sentinel's handle number is that sentinel: the same kind, object
// type, full name and content. A different object at a reused number, a
// wrong type or an unreadable object is not the sentinel. An error means the
// sentinels or the report cannot support any decision.
func InheritedSentinels(sent, observed []ObjectIdentity) ([]bool, error) {
	if len(observed) != len(sent) {
		return nil, fmt.Errorf("%d observations for %d sentinels", len(observed), len(sent))
	}
	out := make([]bool, len(sent))
	for i, s := range sent {
		if !s.complete() {
			return nil, fmt.Errorf("sentinel %d has no complete identity", i)
		}
		if observed[i].Kind != s.Kind {
			return nil, fmt.Errorf("observation %d is for another kind", i)
		}
		out[i] = observed[i] == s
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
	event := ObjectIdentity{Kind: SentinelEvent, Type: "Event", Name: `\BaseNamedObjects\` + SentinelName(SentinelEvent, "n1")}
	section := ObjectIdentity{Kind: SentinelSection, Type: "Section", Name: `\BaseNamedObjects\` + SentinelName(SentinelSection, "n2"), Content: "c2"}
	sent := []ObjectIdentity{event, section}
	got, err := InheritedSentinels(sent, []ObjectIdentity{event, section})
	if err != nil || !got[0] || !got[1] {
		t.Fatalf("exact identities %v %v", got, err)
	}
	// Each observation at a sentinel's number that is another object, or
	// none, is not that sentinel; the other sentinel's decision stands.
	other := `\Sessions\2\BaseNamedObjects\` + SentinelName(SentinelSection, "n2")
	for name, c := range map[string]struct {
		observed [2]ObjectIdentity
		want     [2]bool
	}{
		"no object at the numbers":       {[2]ObjectIdentity{{Kind: SentinelEvent}, {Kind: SentinelSection}}, [2]bool{}},
		"another event reusing a number": {[2]ObjectIdentity{{Kind: SentinelEvent, Type: "Event", Name: `\BaseNamedObjects\other`}, section}, [2]bool{false, true}},
		"an unnamed event":               {[2]ObjectIdentity{{Kind: SentinelEvent, Type: "Event"}, section}, [2]bool{false, true}},
		"a file at the event's number":   {[2]ObjectIdentity{{Kind: SentinelEvent, Type: "File"}, section}, [2]bool{false, true}},
		"a mutex with the event's name":  {[2]ObjectIdentity{{Kind: SentinelEvent, Type: "Mutant", Name: event.Name}, section}, [2]bool{false, true}},
		"a section with other contents":  {[2]ObjectIdentity{event, {Kind: SentinelSection, Type: "Section", Name: section.Name, Content: "other"}}, [2]bool{true, false}},
		"a section whose view failed":    {[2]ObjectIdentity{event, {Kind: SentinelSection, Type: "Section", Name: section.Name}}, [2]bool{true, false}},
		"a section in another namespace": {[2]ObjectIdentity{event, {Kind: SentinelSection, Type: "Section", Name: other, Content: "c2"}}, [2]bool{true, false}},
		"an event posing as the section": {[2]ObjectIdentity{event, {Kind: SentinelSection, Type: "Event", Name: section.Name, Content: "c2"}}, [2]bool{true, false}},
	} {
		got, err := InheritedSentinels(sent, c.observed[:])
		if err != nil || got[0] != c.want[0] || got[1] != c.want[1] {
			t.Errorf("%s: decisions %v %v, want %v", name, got, err, c.want)
		}
	}
	for name, c := range map[string]struct{ sent, observed []ObjectIdentity }{
		"missing observation":         {sent, []ObjectIdentity{event}},
		"extra observation":           {sent, []ObjectIdentity{event, section, event}},
		"observation of another kind": {sent, []ObjectIdentity{section, event}},
		"sentinel without a name":     {[]ObjectIdentity{{Kind: SentinelEvent, Type: "Event"}}, []ObjectIdentity{{Kind: SentinelEvent, Type: "Event"}}},
		"section without content":     {[]ObjectIdentity{{Kind: SentinelSection, Type: "Section", Name: "s"}}, []ObjectIdentity{{Kind: SentinelSection, Type: "Section", Name: "s"}}},
		"sentinel of a wrong type":    {[]ObjectIdentity{{Kind: SentinelEvent, Type: "Mutant", Name: "e"}}, []ObjectIdentity{{Kind: SentinelEvent, Type: "Mutant", Name: "e"}}},
		"unknown kind":                {[]ObjectIdentity{{Kind: "mutex", Type: "Mutant", Name: "m"}}, []ObjectIdentity{{Kind: "mutex", Type: "Mutant", Name: "m"}}},
		"empty identities":            {[]ObjectIdentity{{}}, []ObjectIdentity{{}}},
	} {
		if _, err := InheritedSentinels(c.sent, c.observed); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
