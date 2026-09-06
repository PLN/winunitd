package unit

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
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
			name:    "null is not a string argument",
			exec:    `["C:\\Tools\\foo.exe", null]`,
			wantErr: true,
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

func TestExecStartArgFootgun(t *testing.T) {
	t.Parallel()

	existing := `C:\Program Files\Foo\foo.exe`
	stat := func(name string) (os.FileInfo, error) {
		if name == existing {
			return regularFileInfo(name), nil
		}
		if name == `C:\Program Files\Foo` {
			return dirFileInfo(name), nil
		}
		return nil, os.ErrNotExist
	}

	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "leftover flags", in: `C:\App\foo.exe --verbose`, want: true},
		{name: "leftover positional", in: `C:\App\foo.exe config.json`, want: true},
		{name: "spaced path that stats", in: existing, want: false},
		{name: "spaced path that does not stat", in: `C:\Program Files\Missing\foo.exe`, want: true},
		{name: "no whitespace", in: `C:\Tools\foo.exe`, want: false},
		{name: "json array", in: `["C:\\Tools\\foo.exe", "--one"]`, want: false},
		{name: "quoted path", in: `"C:\Program Files\Foo\foo.exe"`, want: false},
		{name: "directory is not a file", in: `C:\Program Files\Foo`, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := execStartArgFootgun(tt.in, stat); got != tt.want {
				t.Fatalf("execStartArgFootgun(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestExecStartArgFootgunRealStat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	spacedDir := filepath.Join(dir, "Program Files")
	if err := os.MkdirAll(spacedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(spacedDir, "foo.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if execStartArgFootgun(exe, nil) {
		t.Fatalf("existing file with spaces should not be a footgun")
	}
	if !execStartArgFootgun(exe+" --verbose", nil) {
		t.Fatalf("existing path plus leftover args should be a footgun")
	}
	missingSpaced := filepath.Join(dir, "Missing Files", "foo.exe")
	if !execStartArgFootgun(missingSpaced, nil) {
		t.Fatalf("missing spaced path should be a footgun")
	}
	if execStartArgFootgun(filepath.Join(dir, "foo.exe"), nil) {
		t.Fatalf("path without unquoted whitespace is not a footgun")
	}
}

func regularFileInfo(name string) os.FileInfo {
	return fileInfo{name: name}
}

func dirFileInfo(name string) os.FileInfo {
	return fileInfo{name: name, dir: true}
}

type fileInfo struct {
	name string
	dir  bool
}

func (f fileInfo) Name() string { return f.name }
func (f fileInfo) Size() int64  { return 1 }
func (f fileInfo) Mode() os.FileMode {
	if f.dir {
		return os.ModeDir | 0o755
	}
	return 0o644
}
func (f fileInfo) ModTime() time.Time { return time.Time{} }
func (f fileInfo) IsDir() bool        { return f.dir }
func (f fileInfo) Sys() any           { return nil }
