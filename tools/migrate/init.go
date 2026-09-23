package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PLN/winunitd/internal/version"
)

const fixtureUnit = "[Unit]\nDescription=Disposable pilot fixture\n\n[Service]\nType=simple\nExecStart=fixture-workload.exe\nRestart=no\n\n[Install]\nWantedBy=default.target\n"

const fixtureEnable = "fixture.service\n"
const fixtureJournal = "fixture-journal\n"

func initFixture(root, scenario string) error {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(scenario) == "" {
		return fmt.Errorf("fixture root and scenario are required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create fixture root")
	}
	if _, err := os.Lstat(filepath.Join(root, fixtureFile)); err == nil {
		return fmt.Errorf("fixture already exists")
	}
	fx, err := scenarioFixture(scenario)
	if err != nil {
		return err
	}
	if err := writeScenarioFiles(root, scenario); err != nil {
		return err
	}
	return saveFixture(root, fx)
}

func scenarioFixture(scenario string) (fixture, error) {
	fx := happyFixture()
	switch scenario {
	case "happy", "linger":
		return fx, nil
	case "custom-base-dir":
		fx.Service = serviceState{Exists: true, Binary: productBinary, BaseDir: `%ProgramData%\winunitd-custom`, LocalSystem: true, Running: true}
		return fx, nil
	case "unmanaged-service":
		fx.Service = serviceState{Exists: true, Binary: `%ProgramFiles%\manual\winunitd.exe`, BaseDir: standardBase, LocalSystem: true, Running: true}
		return fx, nil
	case "non-localsystem":
		fx.Service = serviceState{Exists: true, Binary: productBinary, BaseDir: standardBase, LocalSystem: false, Running: true}
		return fx, nil
	case "beta":
		fx.BetaUpgradeCode = version.BetaUpgradeCode
		return fx, nil
	case "duplicate-launchers":
		fx.Users[0].Tasks = append(fx.Users[0].Tasks, taskState{
			Name: "alice-extra", Command: "winunitd.exe", Enabled: true, Manager: true,
			Definition: taskDefinition("alice", "winunitd.exe"),
		})
		return fx, nil
	case "machine-launcher":
		fx.MachineTasks = []taskState{{
			Name: "machine-pilot", Command: "winunitd.exe", Enabled: true, Manager: true,
			Definition: taskDefinition("SYSTEM", "winunitd.exe"),
		}}
		return fx, nil
	case "machine-run":
		fx.MachineRun = []runValue{{Name: "winunitd", Command: "winunitd.exe"}}
		return fx, nil
	case "reparse":
		fx.DataReparse = true
		return fx, nil
	case "unsafe-data":
		fx.DataUnsafe = true
		return fx, nil
	case "collision":
		return collisionFixture(), nil
	default:
		return fixture{}, fmt.Errorf("unknown fixture scenario")
	}
}

func happyFixture() fixture {
	return fixture{
		MachineTasks: []taskState{},
		MachineRun:   []runValue{},
		Users: []userState{
			{
				Name: "alice",
				Tasks: []taskState{
					{Name: "alice-pilot", Command: "winunitd.exe", Enabled: true, Manager: true, Definition: taskDefinition("alice", "winunitd.exe")},
					{Name: "alice-trigger", Command: "fixture-workload.exe", Enabled: true, Manager: false, Definition: taskDefinition("alice", "fixture-workload.exe")},
				},
				Processes: []processState{{Command: "winunitd.exe", Running: true}},
				PilotRel:  "users/alice/pilot",
				DestRel:   "users/alice/local/winunitd",
			},
			{
				Name:      "bob",
				Tasks:     []taskState{},
				Processes: []processState{},
				PilotRel:  "users/bob/pilot",
				DestRel:   "users/bob/local/winunitd",
			},
		},
	}
}

func collisionFixture() fixture {
	return fixture{
		MachineTasks: []taskState{},
		MachineRun:   []runValue{},
		Users: []userState{{
			Name: "carol",
			Tasks: []taskState{{
				Name: "carol-pilot", Command: "winunitd.exe", Enabled: true, Manager: true,
				Definition: taskDefinition("carol", "winunitd.exe"),
			}},
			Processes: []processState{},
			PilotRel:  "users/carol/pilot",
			DestRel:   "users/carol/local/winunitd",
		}},
	}
}

func taskDefinition(account, command string) string {
	return "<Task><Principals><Principal><UserId>" + account + "</UserId><LogonType>InteractiveToken</LogonType></Principal></Principals><Actions><Exec><Command>" + command + "</Command></Exec></Actions></Task>"
}

func writeScenarioFiles(root, scenario string) error {
	if scenario == "collision" {
		if err := writePilotTree(root, "users/carol/pilot", false); err != nil {
			return err
		}
		return writeNew(filepath.Join(root, "users", "carol", "local", "winunitd", "units", "fixture.service"), []byte(fixtureUnit+"# existing\n"))
	}
	if err := writePilotTree(root, "users/alice/pilot", scenario == "linger"); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(root, "users", "bob", "pilot"), 0o755)
}

func writePilotTree(root, rel string, linger bool) error {
	base := filepath.Join(root, filepath.FromSlash(rel))
	files := map[string]string{
		"units/fixture.service":                  fixtureUnit,
		"enabled/default.target/fixture.service": fixtureEnable,
		"journal/fixture.log":                    fixtureJournal,
	}
	if linger {
		files["linger/record"] = "opt-in\n"
	}
	for name, body := range files {
		if err := writeNew(filepath.Join(base, filepath.FromSlash(name)), []byte(body)); err != nil {
			return err
		}
	}
	return nil
}
