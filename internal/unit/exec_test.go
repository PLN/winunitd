package unit

import (
	"reflect"
	"testing"
)

func TestBuildArgv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		exec    string
		args    []string
		want    []string
		wantErr bool
	}{
		{
			name: "space-separated compatibility",
			exec: `C:\Tools\foo.exe --bar`,
			want: []string{`C:\Tools\foo.exe`, "--bar"},
		},
		{
			name: "quoted exe with spaces",
			exec: `"C:\Program Files\Foo\foo.exe" --listen 127.0.0.1:8080`,
			want: []string{`C:\Program Files\Foo\foo.exe`, "--listen", "127.0.0.1:8080"},
		},
		{
			name: "json array",
			exec: `["C:\\Program Files\\Foo\\foo.exe", "--listen", "127.0.0.1:8080"]`,
			want: []string{`C:\Program Files\Foo\foo.exe`, "--listen", "127.0.0.1:8080"},
		},
		{
			name: "execstartarg leaves spaces in exe",
			exec: `C:\Program Files\Foo\foo.exe`,
			args: []string{"--listen", "127.0.0.1:8080"},
			want: []string{`C:\Program Files\Foo\foo.exe`, "--listen", "127.0.0.1:8080"},
		},
		{
			name: "json plus execstartarg",
			exec: `["C:\\Tools\\foo.exe", "--one"]`,
			args: []string{"--two"},
			want: []string{`C:\Tools\foo.exe`, "--one", "--two"},
		},
		{name: "empty", exec: "", wantErr: true},
		{name: "empty json", exec: "[]", wantErr: true},
		{name: "unterminated quote", exec: `"C:\Tools\foo.exe`, wantErr: true},
		{name: "json not strings", exec: `[1, 2]`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := buildArgv(tt.exec, tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("buildArgv succeeded, want error")
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

func TestWindowsAbs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want bool
	}{
		{`C:\Tools\foo.exe`, true},
		{`C:/Tools/foo.exe`, true},
		{`\\server\share\foo.exe`, true},
		{`foo.exe`, false},
		{`.\foo.exe`, false},
		{`Tools\foo.exe`, false},
		{`/usr/bin/foo`, false},
		{``, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := WindowsAbs(tt.in); got != tt.want {
				t.Fatalf("WindowsAbs(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
