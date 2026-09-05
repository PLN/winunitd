package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReconcileRequiresInventoryConfiguration(t *testing.T) {
	for _, c := range []config{{}, {StateDir: t.TempDir()}, {StateDir: t.TempDir(), FirstVMID: 200, LastVMID: 199, Storage: "test"}} {
		if err := reconcile(nil, c); err == nil {
			t.Fatal("incomplete inventory configuration accepted")
		}
	}
}

func TestReconcileDetectsUnsafeInventoryWithoutMutation(t *testing.T) {
	for _, scenario := range []string{"owned", "expired", "reused", "absent", "orphan", "unmanaged"} {
		t.Run(scenario, func(t *testing.T) {
			c := config{StateDir: t.TempDir(), Node: "lab", Pool: "test", Storage: "test", FirstVMID: 200, LastVMID: 210}
			r := runRecord{Schema: 1, ID: "0123456789abcdef0123456789abcdef", Node: c.Node, Pool: c.Pool, VMID: 200, Created: time.Now().UTC()}
			r.Marker = "winunitd-lab-run:" + r.ID
			if scenario == "expired" {
				r.Created = r.Created.Add(-25 * time.Hour)
			}
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(c.StateDir, r.ID+".json")
			if scenario != "unmanaged" {
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			a := testAPI(t, func(w http.ResponseWriter, req *http.Request) {
				if req.Method != http.MethodGet {
					t.Errorf("audit attempted mutation: %s", req.Method)
					w.WriteHeader(405)
					return
				}
				switch req.URL.Path {
				case "/api2/json/pools/test":
					if scenario == "absent" || scenario == "orphan" {
						fmt.Fprint(w, `{"data":{"members":[]}}`)
					} else {
						fmt.Fprint(w, `{"data":{"members":[{"vmid":200,"node":"lab","type":"qemu"}]}}`)
					}
				case "/api2/json/nodes/lab/qemu/200/config":
					marker := r.Marker
					if scenario == "reused" {
						marker = "another-run"
					}
					fmt.Fprintf(w, `{"data":{"description":%q}}`, marker)
				case "/api2/json/nodes/lab/storage/test/content":
					if req.URL.Query().Get("vmid") != "200" || req.URL.Query().Get("content") != "images" {
						t.Error("unscoped storage query")
					}
					if scenario == "orphan" {
						fmt.Fprint(w, `{"data":[{"volid":"test:200/vm-200-disk-0.raw"}]}`)
					} else {
						fmt.Fprint(w, `{"data":[]}`)
					}
				default:
					t.Errorf("unexpected request %s", req.URL.Path)
					w.WriteHeader(404)
				}
			})
			err = reconcile(a, c)
			wantOK := scenario == "owned" || scenario == "absent"
			if (err == nil) != wantOK {
				t.Fatalf("inventory outcome: %v", err)
			}
			if scenario != "unmanaged" {
				after, err := os.ReadFile(path)
				if err != nil || string(after) != string(data) {
					t.Fatal("audit changed private record")
				}
			}
		})
	}
}
