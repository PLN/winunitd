package main

import "testing"

func TestRetirementRejectsForeignResources(t *testing.T) {
	for _, scenario := range []string{"owned", "other-guest-disk", "other-storage", "extra-disk", "attached-media", "extra-network", "passthrough", "hook"} {
		t.Run(scenario, func(t *testing.T) {
			vm := map[string]any{"sata0": "test:200/vm-200-disk-1.raw,size=80G", "efidisk0": "test:200/vm-200-disk-0.raw", "tpmstate0": "test:200/vm-200-disk-2.raw", "net0": "bridge=isolated"}
			switch scenario {
			case "other-guest-disk":
				vm["sata0"] = "test:201/vm-201-disk-1.raw"
			case "other-storage":
				vm["sata0"] = "prod:200/vm-200-disk-1.raw"
			case "extra-disk":
				vm["scsi1"] = "test:200/vm-200-disk-3.raw"
			case "attached-media":
				vm["sata2"] = "test:iso/bootstrap.iso,media=cdrom"
			case "extra-network":
				vm["net1"] = "bridge=prod"
			case "passthrough":
				vm["hostpci0"] = "00:00.0"
			case "hook":
				vm["hookscript"] = "test:snippets/hook.sh"
			}
			err := retirementDevices(vm, config{Storage: "test"}, 200)
			if (err == nil) != (scenario == "owned") {
				t.Fatalf("retirement admission: %v", err)
			}
		})
	}
}
