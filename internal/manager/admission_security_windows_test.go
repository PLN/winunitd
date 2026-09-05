package manager

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestAdmissionPolicySecurity(t *testing.T) {
	for _, tc := range []struct {
		sddl  string
		allow bool
	}{
		{"O:BAG:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FR;;;BU)", true},
		{"O:SYG:SYD:P(A;;FA;;;SY)(A;;FA;;;BA)", true},
		{"O:BAG:BAD:P(A;;FA;;;SY)(A;;FA;;;BU)", false},
		{"O:BAG:BAD:P(A;;WD;;;BU)", false},
		{"O:BUG:BUD:P(A;;FA;;;SY)(A;;FA;;;BA)", false},
	} {
		t.Run(tc.sddl, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatal(err)
			}
			err = admissionSecurityTrusted(sd)
			if (err == nil) != tc.allow {
				t.Fatalf("allowed=%v error=%v", tc.allow, err)
			}
		})
	}
}
