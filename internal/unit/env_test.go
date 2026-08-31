package unit

import (
	"reflect"
	"testing"
)

func TestParseEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    []EnvVar
		wantErr bool
	}{
		{
			name: "single",
			in:   "FOO=bar",
			want: []EnvVar{{Name: "FOO", Value: "bar"}},
		},
		{
			name: "literal dollar braces",
			in:   "FOO=${BAR}",
			want: []EnvVar{{Name: "FOO", Value: "${BAR}"}},
		},
		{
			name: "percent windows style also literal",
			in:   "FOO=%BAR%",
			want: []EnvVar{{Name: "FOO", Value: "%BAR%"}},
		},
		{
			name: "quoted value with space",
			in:   `"FOO=bar baz" ABC=123`,
			want: []EnvVar{{Name: "FOO", Value: "bar baz"}, {Name: "ABC", Value: "123"}},
		},
		{
			name:    "unquoted value with space is extra tokens",
			in:      "FOO=hello from journal",
			wantErr: true,
		},
		{
			name:    "missing equals",
			in:      "FOOBAR",
			wantErr: true,
		},
		{
			name:    "empty name",
			in:      "=bar",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseEnvironment(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}
