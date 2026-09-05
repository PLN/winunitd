package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuestFailurePreservesPrivateDiagnostics(t *testing.T) {
	for _, scenario := range []string{"exit", "signal", "stdout-truncated", "stderr-truncated"} {
		t.Run(scenario, func(t *testing.T) {
			c := config{Node: "lab", StateDir: t.TempDir()}
			a := testAPI(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					_, _ = w.Write([]byte(`{"data":{"pid":42}}`))
					return
				}
				status := map[string]any{"exited": 1, "exitcode": 0, "out-data": "private stdout", "err-data": "private stderr"}
				switch scenario {
				case "exit":
					status["exitcode"] = 1
				case "signal":
					status["signal"] = 9
				case "stdout-truncated":
					status["out-truncated"] = 1
				case "stderr-truncated":
					status["err-truncated"] = true
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": status})
			})
			out, err := guestExec(context.Background(), a, c, 200, "fixture command")
			if err == nil || out != "" || strings.Contains(err.Error(), "private stdout") || strings.Contains(err.Error(), "private stderr") {
				t.Fatalf("failure escaped private diagnostics: %q, %v", out, err)
			}
			files, err := filepath.Glob(filepath.Join(c.StateDir, "guest-failure-*.json"))
			if err != nil || len(files) != 1 {
				t.Fatal("private diagnostic missing")
			}
			raw, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "private stdout") || !strings.Contains(string(raw), "private stderr") || strings.Contains(string(raw), "fixture command") {
				t.Fatal("diagnostic omitted output or stored command text")
			}
		})
	}
}

func TestOwnershipRejectsReusedGuestAndDrift(t *testing.T) {
	for _, scenario := range []string{"owned", "reused-id", "other-pool", "other-node", "other-network", "extra-network", "missing-record"} {
		t.Run(scenario, func(t *testing.T) {
			c := config{Node: "lab", Pool: "test", Bridge: "isolated", FirstVMID: 200, LastVMID: 209, StateDir: t.TempDir()}
			r := runRecord{Schema: 1, ID: "0123456789abcdef0123456789abcdef", Node: c.Node, Pool: c.Pool, VMID: 200}
			r.Marker = "winunitd-lab-run:" + r.ID
			if scenario != "missing-record" {
				if err := saveRecord(filepath.Join(c.StateDir, r.ID+".json"), r); err != nil {
					t.Fatal(err)
				}
			}
			a := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet {
					t.Error("ownership check mutated guest")
					return
				}
				var data any
				switch req.URL.Path {
				case "/api2/json/pools/test":
					node := "lab"
					if scenario == "other-node" {
						node = "prod"
					}
					members := []map[string]any{{"vmid": 200, "node": node}}
					if scenario == "other-pool" {
						members = nil
					}
					data = map[string]any{"members": members}
				case "/api2/json/nodes/lab/qemu/200/config":
					marker, bridge := r.Marker, "isolated"
					if scenario == "reused-id" {
						marker = "winunitd-lab-run:fedcba9876543210fedcba9876543210"
					}
					if scenario == "other-network" {
						bridge = "prod"
					}
					vm := map[string]string{"description": marker, "net0": "e1000=02:00:00:00:00:01,bridge=" + bridge + ",firewall=1"}
					if scenario == "extra-network" {
						vm["net1"] = "bridge=prod"
					}
					data = vm
				default:
					t.Errorf("unexpected endpoint")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			})
			_, _, err := ownedGuest(context.Background(), a, c, 200)
			if (err == nil) != (scenario == "owned") {
				t.Fatalf("ownership outcome: %v", err)
			}
		})
	}
}

func TestDetachRequiresLiveConfigurationRemoval(t *testing.T) {
	for _, pending := range []bool{false, true} {
		c := config{Node: "lab", Pool: "test", Bridge: "isolated", FirstVMID: 200, LastVMID: 209, StateDir: t.TempDir()}
		r := runRecord{Schema: 1, ID: "0123456789abcdef0123456789abcdef", Node: "lab", Pool: "test", VMID: 200, State: "ready", OSISO: "test:iso/windows.iso", BootstrapISO: "test:iso/bootstrap.iso"}
		r.Marker = "winunitd-lab-run:" + r.ID
		path := filepath.Join(c.StateDir, r.ID+".json")
		if err := saveRecord(path, r); err != nil {
			t.Fatal(err)
		}
		changed := false
		a := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
			var data any
			if req.URL.Path == "/api2/json/pools/test" {
				data = map[string]any{"members": []map[string]any{{"vmid": 200, "node": "lab"}}}
			} else if req.Method == http.MethodPut {
				changed = true
			} else {
				vm := map[string]string{"description": r.Marker, "net0": "bridge=isolated", "digest": "fixture"}
				if !changed || pending {
					vm["sata1"] = r.OSISO + ",media=cdrom"
					vm["sata2"] = r.BootstrapISO + ",media=cdrom"
				}
				if changed && req.URL.Query().Get("current") != "1" {
					t.Error("live configuration was not requested")
				}
				data = vm
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		})
		err := detachMedia(a, c, 200)
		if (err != nil) != pending {
			t.Fatalf("pending=%v: %v", pending, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		if r.MediaDetached == pending {
			t.Fatal("persisted detached claim disagrees with live configuration")
		}
	}
}

func TestAdmissionRejectsThirdRunningGuest(t *testing.T) {
	c := config{Node: "lab", Pool: "test", Storage: "test", Bridge: "isolated", MACPrefix: "02:00:00", FirstVMID: 200, LastVMID: 209, StateDir: t.TempDir()}
	a := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			t.Error("concurrency rejection allocated resources")
		}
		data := `{"data":{"status":"running"}}`
		if req.URL.Path == "/api2/json/pools/test" {
			data = `{"data":{"members":[{"type":"qemu","vmid":201,"node":"lab"},{"type":"qemu","vmid":202,"node":"lab"}]}}`
		}
		_, _ = w.Write([]byte(data))
	})
	if err := createGuest(a, c, 203, "test:iso/windows.iso", "test:iso/bootstrap.iso"); err == nil {
		t.Fatal("third running guest admitted")
	}
	files, err := filepath.Glob(filepath.Join(c.StateDir, "*"))
	if err != nil || len(files) != 0 {
		t.Fatal("rejected admission left state or lock")
	}
}
