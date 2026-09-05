package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

func TestOwnershipRejectsReusedGuestAndDrift(t *testing.T) {
	for _, scenario := range []string{"owned", "reused-id", "other-pool", "other-node", "other-network", "missing-record"} {
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
					t.Fatal("ownership check mutated guest")
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
					data = map[string]string{"description": marker, "net0": "e1000=02:00:00:00:00:01,bridge=" + bridge + ",firewall=1"}
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
