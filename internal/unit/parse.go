package unit

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/eventlog"
	"github.com/PLN/winunitd/internal/pathwatch"
	"github.com/PLN/winunitd/internal/registry"
	"github.com/PLN/winunitd/internal/timers"
)

var knownDirectives = map[string]map[string]bool{
	"Unit": {
		"FormatVersion":              true,
		"Description":                true,
		"Requires":                   true,
		"Wants":                      true,
		"BindsTo":                    true,
		"PartOf":                     true,
		"After":                      true,
		"Before":                     true,
		"RequiresInteractiveSession": true,
		"StartLimitIntervalSec":      true,
		"StartLimitBurst":            true,
	},
	"Service": {
		"Type":                     true,
		"RemainAfterExit":          true,
		"ServiceName":              true,
		"TaskName":                 true,
		"ExecStart":                true,
		"ExecStartArg":             true,
		"ExecStop":                 true,
		"ExecStopArg":              true,
		"WorkingDirectory":         true,
		"Environment":              true,
		"Restart":                  true,
		"RestartSec":               true,
		"RestartBackoff":           true,
		"RestartMaxDelaySec":       true,
		"TimeoutStartSec":          true,
		"TimeoutStopSec":           true,
		"NotifyAccess":             true,
		"WatchdogSec":              true,
		"WatchdogMode":             true,
		"WatchdogEndpoint":         true,
		"WatchdogExpectedStatus":   true,
		"MemoryMax":                true,
		"ProcessLimit":             true,
		"PriorityClass":            true,
		"CPUWeight":                true,
		"WindowsCPUWeight":         true,
		"WindowsCPUQuota":          true,
		"CPUQuota":                 true,
		"IoPriority":               true,
		"WatchdogGraceSec":         true,
		"WatchdogTimeoutSec":       true,
		"WatchdogFailureThreshold": true,
	},
	"Timer": {
		"OnBootSec":       true,
		"OnStartupSec":    true,
		"OnUnitActiveSec": true,
		"OnCalendar":      true,
		"Persistent":      true,
		"Unit":            true,
	},
	"Registry": {
		"RegistryChanged": true,
	},
	"EventLog": {
		"EventLogTrigger": true,
	},
	"Path": {
		"PathChanged":   true,
		"PathExists":    true,
		"PathExistsAll": true,
	},
	"Install": {
		"WantedBy": true,
	},
}

var sectionsByKind = map[Kind]map[string]bool{
	KindService:  {"Unit": true, "Service": true, "Install": true},
	KindTimer:    {"Unit": true, "Timer": true, "Install": true},
	KindTarget:   {"Unit": true, "Install": true},
	KindRegistry: {"Unit": true, "Registry": true, "Install": true},
	KindEventLog: {"Unit": true, "EventLog": true, "Install": true},
	KindPath:     {"Unit": true, "Path": true, "Install": true},
}

type serviceBuilder struct {
	remainAfterExit     string
	remainAfterExitLine int

	typ          string
	typLine      int
	execRaw      string
	execLine     int
	execSet      bool
	execArgs     []string
	execStopRaw  string
	execStopLine int
	execStopSet  bool
	execStopArgs []string
	wd           string
	wdLine       int
	wdSet        bool
	env          []EnvVar
	restart      string
	restLine     int
	restSet      bool

	restartSec       string
	restartSecL      int
	restartBackoff   string
	restartBackoffL  int
	restartMaxDelay  string
	restartMaxDelayL int
	timeoutStart     string
	timeoutStartL    int
	timeoutStop      string
	timeoutStopL     int

	serviceName  string
	serviceNameL int
	taskName     string
	taskNameL    int

	notifyAccess       string
	notifyAccessL      int
	watchdogSec        string
	watchdogSecL       int
	watchdogGrace      string
	watchdogGraceL     int
	watchdogTimeout    string
	watchdogTimeoutL   int
	watchdogThreshold  string
	watchdogThresholdL int
	watchdogMode       string
	watchdogModeL      int
	watchdogEP         string
	watchdogEPL        int
	watchdogStat       string
	watchdogStatL      int

	memoryMax         string
	memoryMaxL        int
	processLimit      string
	processLimitL     int
	priorityClass     string
	priorityClassL    int
	windowsCPUWeight  string
	windowsCPUWeightL int
	windowsCPUQuota   string
	windowsCPUQuotaL  int
	cpuWeight         string
	cpuWeightL        int
	cpuQuota          string
	cpuQuotaL         int
	ioPriority        string
	ioPriorityL       int
}

type timerBuilder struct {
	onBoot        string
	onBootL       int
	onStartup     string
	onStartupL    int
	onUnitActive  string
	onUnitActiveL int
	calendars     []string
	calendarLines []int
	persistent    string
	persistentL   int
	persistentSet bool
	unit          string
	unitL         int
	unitSet       bool
}

type registryBuilder struct {
	changed []string
	lines   []int
}

type eventLogBuilder struct {
	triggers []string
	lines    []int
}

type pathBuilder struct {
	existsAll    bool
	existsAny    bool
	changed      []string
	changedLines []int
	exists       []string
	existsLines  []int
}

type parser struct {
	formatLine int
	path       string
	name       string
	kind       Kind
	unit       *Unit
	issues     []Issue

	svc   *serviceBuilder
	timer *timerBuilder
	reg   *registryBuilder
	evt   *eventLogBuilder
	pth   *pathBuilder

	ris     string
	risLine int
	risSet  bool

	startLimitInterval  string
	startLimitIntervalL int
	startLimitBurst     string
	startLimitBurstL    int

	stat func(string) (os.FileInfo, error)
}

func (p *parser) errorf(line int, format string, args ...any) {
	p.issues = append(p.issues, Issue{
		Path:     p.path,
		Line:     line,
		Severity: SeverityError,
		Message:  fmt.Sprintf(format, args...),
	})
}

func (p *parser) warnf(line int, format string, args ...any) {
	p.issues = append(p.issues, Issue{
		Path:     p.path,
		Line:     line,
		Severity: SeverityWarning,
		Message:  fmt.Sprintf(format, args...),
	})
}

// Parse parses and verifies a unit file from memory.
// name is the unit file name (foo.service); it is stored lower-case
// (DESIGN.md §36). path is used in diagnostics and may keep on-disk case.
func Parse(path, name string, src []byte) Report {
	return parseReport(path, name, src, nil)
}

func parseReport(path, name string, src []byte, stat func(string) (os.FileInfo, error)) Report {
	kind, err := KindFromName(name)
	if err != nil {
		return Report{Issues: []Issue{{
			Path:     path,
			Severity: SeverityError,
			Message:  err.Error(),
		}}}
	}

	canon := NormalizeName(name)
	p := &parser{
		path: path,
		name: canon,
		kind: kind,
		unit: &Unit{
			FormatVersion: 1,
			Name:          canon,
			Path:          path,
			Kind:          kind,
		},
		stat: stat,
	}

	doc, iniIssues := parseINI(src, path)
	p.issues = append(p.issues, iniIssues...)

	allowed := sectionsByKind[kind]
	for _, sec := range doc.sections {
		if !allowed[sec.name] {
			if _, known := knownDirectives[sec.name]; known {
				p.errorf(sec.line, "section [%s] is not valid in a %s unit", sec.name, kind)
			} else {
				p.errorf(sec.line, "unknown section [%s]", sec.name)
			}
			// Still walk entries so unknown directives inside are also reported.
		}
		known := knownDirectives[sec.name]
		if known == nil {
			for _, e := range sec.entries {
				p.errorf(e.line, "unknown directive %q in section [%s]", e.key, sec.name)
			}
			continue
		}
		if sec.name == "Registry" && p.reg == nil {
			p.reg = &registryBuilder{}
		}
		if sec.name == "EventLog" && p.evt == nil {
			p.evt = &eventLogBuilder{}
		}
		if sec.name == "Path" && p.pth == nil {
			p.pth = &pathBuilder{}
		}
		for _, e := range sec.entries {
			if !known[e.key] {
				p.errorf(e.line, "unknown directive %q in section [%s]", e.key, sec.name)
				continue
			}
			p.apply(sec.name, e)
		}
	}

	p.finish()
	return Report{Unit: p.unit, Issues: p.issues}
}

// ParseUnit parses a unit named name from src. Diagnostics use name as the path.
func ParseUnit(name, src string) Report {
	return Parse(name, name, []byte(src))
}

func (p *parser) apply(section string, e iniEntry) {
	switch section {
	case "Unit":
		p.applyUnit(e)
	case "Service":
		if p.svc == nil {
			p.svc = &serviceBuilder{typ: string(TypeSimple), restart: string(RestartNo)}
		}
		p.applyService(e)
	case "Timer":
		if p.timer == nil {
			p.timer = &timerBuilder{}
		}
		p.applyTimer(e)
	case "Registry":
		if p.reg == nil {
			p.reg = &registryBuilder{}
		}
		p.applyRegistry(e)
	case "EventLog":
		if p.evt == nil {
			p.evt = &eventLogBuilder{}
		}
		p.applyEventLog(e)
	case "Path":
		if p.pth == nil {
			p.pth = &pathBuilder{}
		}
		p.applyPath(e)
	case "Install":
		p.applyInstall(e)
	}
}

func (p *parser) applyUnit(e iniEntry) {
	switch e.key {
	case "FormatVersion":
		if p.formatLine != 0 {
			p.errorf(e.line, "FormatVersion may be specified only once")
		}
		p.formatLine = e.line
		switch e.value {
		case "1":
			p.unit.FormatVersion = 1
		case "2":
			p.unit.FormatVersion = 2
		default:
			p.errorf(e.line, "unsupported FormatVersion %q (supported: 1, 2)", e.value)
		}
	case "Description":
		p.unit.Description = e.value
	case "Requires":
		p.unit.Requires = applyList(p.unit.Requires, e.value)
	case "Wants":
		p.unit.Wants = applyList(p.unit.Wants, e.value)
	case "BindsTo":
		p.unit.BindsTo = applyList(p.unit.BindsTo, e.value)
	case "PartOf":
		p.unit.PartOf = applyList(p.unit.PartOf, e.value)
	case "After":
		p.unit.After = applyList(p.unit.After, e.value)
	case "Before":
		p.unit.Before = applyList(p.unit.Before, e.value)
	case "RequiresInteractiveSession":
		p.ris = e.value
		p.risLine = e.line
		p.risSet = true
	case "StartLimitIntervalSec":
		p.startLimitInterval = e.value
		p.startLimitIntervalL = e.line
	case "StartLimitBurst":
		p.startLimitBurst = e.value
		p.startLimitBurstL = e.line
	}
}

func (p *parser) applyService(e iniEntry) {
	s := p.svc
	switch e.key {
	case "Type":
		s.typ = e.value
		s.typLine = e.line
	case "RemainAfterExit":
		s.remainAfterExit = e.value
		s.remainAfterExitLine = e.line
	case "ServiceName":
		s.serviceName = e.value
		s.serviceNameL = e.line
	case "TaskName":
		s.taskName = e.value
		s.taskNameL = e.line
	case "ExecStart":
		s.execRaw = e.value
		s.execLine = e.line
		s.execSet = true
	case "ExecStartArg":
		s.execArgs = append(s.execArgs, e.value)
	case "ExecStop":
		if s.execStopSet {
			p.errorf(e.line, "multiple ExecStop commands are not supported")
		}
		s.execStopRaw, s.execStopLine, s.execStopSet = e.value, e.line, true
	case "ExecStopArg":
		s.execStopArgs = append(s.execStopArgs, e.value)
	case "WorkingDirectory":
		s.wd = e.value
		s.wdLine = e.line
		s.wdSet = true
	case "Environment":
		if e.value == "" {
			s.env = nil
			return
		}
		vars, err := parseEnvironment(e.value)
		if err != nil {
			p.errorf(e.line, "%s", err.Error())
			return
		}
		s.env = append(s.env, vars...)
	case "Restart":
		s.restart = e.value
		s.restLine = e.line
		s.restSet = true
	case "RestartBackoff":
		s.restartBackoff, s.restartBackoffL = e.value, e.line
	case "RestartMaxDelaySec":
		s.restartMaxDelay, s.restartMaxDelayL = e.value, e.line
	case "RestartSec":
		s.restartSec = e.value
		s.restartSecL = e.line
	case "TimeoutStartSec":
		s.timeoutStart = e.value
		s.timeoutStartL = e.line
	case "TimeoutStopSec":
		s.timeoutStop = e.value
		s.timeoutStopL = e.line
	case "NotifyAccess":
		s.notifyAccess = e.value
		s.notifyAccessL = e.line
	case "WatchdogGraceSec":
		s.watchdogGrace, s.watchdogGraceL = e.value, e.line
	case "WatchdogTimeoutSec":
		s.watchdogTimeout, s.watchdogTimeoutL = e.value, e.line
	case "WatchdogFailureThreshold":
		s.watchdogThreshold, s.watchdogThresholdL = e.value, e.line
	case "WatchdogSec":
		s.watchdogSec = e.value
		s.watchdogSecL = e.line
	case "WatchdogMode":
		s.watchdogMode = e.value
		s.watchdogModeL = e.line
	case "WatchdogEndpoint":
		s.watchdogEP = e.value
		s.watchdogEPL = e.line
	case "WatchdogExpectedStatus":
		s.watchdogStat = e.value
		s.watchdogStatL = e.line
	case "MemoryMax":
		s.memoryMax = e.value
		s.memoryMaxL = e.line
	case "ProcessLimit":
		s.processLimit = e.value
		s.processLimitL = e.line
	case "PriorityClass":
		s.priorityClass = e.value
		s.priorityClassL = e.line
	case "WindowsCPUWeight":
		s.windowsCPUWeight, s.windowsCPUWeightL = e.value, e.line
	case "WindowsCPUQuota":
		s.windowsCPUQuota, s.windowsCPUQuotaL = e.value, e.line
	case "CPUWeight":
		s.cpuWeight = e.value
		s.cpuWeightL = e.line
	case "CPUQuota":
		s.cpuQuota = e.value
		s.cpuQuotaL = e.line
	case "IoPriority":
		s.ioPriority = e.value
		s.ioPriorityL = e.line
	}
}

func (p *parser) applyTimer(e iniEntry) {
	t := p.timer
	switch e.key {
	case "OnBootSec":
		t.onBoot = e.value
		t.onBootL = e.line
	case "OnStartupSec":
		t.onStartup = e.value
		t.onStartupL = e.line
	case "OnUnitActiveSec":
		t.onUnitActive = e.value
		t.onUnitActiveL = e.line
	case "OnCalendar":
		t.calendars = append(t.calendars, e.value)
		t.calendarLines = append(t.calendarLines, e.line)
	case "Persistent":
		t.persistent = e.value
		t.persistentL = e.line
		t.persistentSet = true
	case "Unit":
		t.unit = e.value
		t.unitL = e.line
		t.unitSet = true
	}
}

func (p *parser) applyRegistry(e iniEntry) {
	r := p.reg
	switch e.key {
	case "RegistryChanged":
		r.changed = append(r.changed, e.value)
		r.lines = append(r.lines, e.line)
	}
}

func (p *parser) applyEventLog(e iniEntry) {
	ev := p.evt
	switch e.key {
	case "EventLogTrigger":
		ev.triggers = append(ev.triggers, e.value)
		ev.lines = append(ev.lines, e.line)
	}
}

func (p *parser) applyPath(e iniEntry) {
	ph := p.pth
	switch e.key {
	case "PathChanged":
		ph.changed = append(ph.changed, e.value)
		ph.changedLines = append(ph.changedLines, e.line)
	case "PathExists", "PathExistsAll":
		if e.key == "PathExistsAll" {
			ph.existsAll = true
		} else {
			ph.existsAny = true
		}
		ph.exists = append(ph.exists, e.value)
		ph.existsLines = append(ph.existsLines, e.line)
	}
}

func (p *parser) applyInstall(e iniEntry) {
	switch e.key {
	case "WantedBy":
		p.unit.WantedBy = applyList(p.unit.WantedBy, e.value)
	}
}

func applyList(cur []string, value string) []string {
	if value == "" {
		return nil
	}
	for _, n := range parseUnitNames(value) {
		n = NormalizeName(n)
		if n == "" {
			continue
		}
		cur = append(cur, n)
	}
	return cur
}

func (p *parser) finish() {
	switch p.kind {
	case KindService:
		p.finishService()
	case KindTimer:
		p.finishTimer()
	case KindRegistry:
		p.finishRegistry()
	case KindEventLog:
		p.finishEventLog()
	case KindPath:
		p.finishPath()
	case KindTarget:
		// targets have no extra required fields
	}
	p.finishInteractiveSession()
	p.finishStartLimit()
}

func (p *parser) finishStartLimit() {
	p.unit.StartLimitInterval = DefaultStartLimitInterval
	p.unit.StartLimitBurst = DefaultStartLimitBurst
	if p.startLimitInterval != "" {
		d, err := ParseDuration(p.startLimitInterval)
		if err != nil {
			p.errorf(p.startLimitIntervalL, "invalid StartLimitIntervalSec: %s", err.Error())
		} else {
			p.unit.StartLimitInterval = d
		}
	}
	if p.startLimitBurst != "" {
		n, err := parseStartLimitBurst(p.startLimitBurst)
		if err != nil {
			p.errorf(p.startLimitBurstL, "%s", err.Error())
		} else {
			p.unit.StartLimitBurst = n
		}
	}
}

func (p *parser) finishInteractiveSession() {
	if !p.risSet {
		return
	}
	b, err := parseBool(p.ris)
	if err != nil {
		p.errorf(p.risLine, "invalid RequiresInteractiveSession: %s", err.Error())
		return
	}
	p.unit.RequiresInteractiveSession = b
}

func (p *parser) finishService() {
	spec := &ServiceSpec{
		Type:    TypeSimple,
		Restart: RestartNo,
	}
	p.unit.Service = spec
	s := p.svc
	if s == nil {
		p.errorf(0, "service unit requires a [Service] section")
		return
	}

	typ := strings.ToLower(strings.TrimSpace(s.typ))
	if typ == "" {
		typ = string(TypeSimple)
	}
	switch ServiceType(typ) {
	case TypeSimple, TypeOneshot, TypeNotify, TypeSCM, TypeScheduledTask:
		spec.Type = ServiceType(typ)
	default:
		p.errorf(s.typLine, "invalid Type %q (supported: simple, oneshot, notify, scm, scheduled-task)", s.typ)
	}

	if s.remainAfterExitLine != 0 {
		value, err := parseBool(s.remainAfterExit)
		if err != nil {
			p.errorf(s.remainAfterExitLine, "invalid RemainAfterExit: %s", err)
		} else {
			spec.RemainAfterExit = value
		}
		if spec.Type != TypeOneshot {
			p.errorf(s.remainAfterExitLine, "RemainAfterExit is only supported for Type=oneshot")
		}
	}

	if spec.Type == TypeSCM {
		name := strings.TrimSpace(s.serviceName)
		if name == "" {
			p.errorf(s.serviceNameL, "ServiceName is required for Type=scm")
		} else {
			spec.ServiceName = name
		}
	} else if strings.TrimSpace(s.serviceName) != "" {
		p.warnf(s.serviceNameL, "ServiceName is only used with Type=scm")
	}

	if spec.Type == TypeScheduledTask {
		name := strings.TrimSpace(s.taskName)
		if name == "" {
			p.errorf(s.taskNameL, "TaskName is required for Type=scheduled-task")
		} else {
			spec.TaskName = name
		}
	} else if strings.TrimSpace(s.taskName) != "" {
		p.warnf(s.taskNameL, "TaskName is only used with Type=scheduled-task")
	}

	hasExec := s.execSet && (strings.TrimSpace(s.execRaw) != "" || len(s.execArgs) > 0)
	if len(s.execArgs) > 0 {
		hasExec = true
	}
	switch spec.Type {
	case TypeScheduledTask:
		if s.execSet || len(s.execArgs) > 0 {
			p.errorf(s.execLine, "ExecStart is not valid for Type=scheduled-task")
		}
	case TypeSCM:
		if hasExec {
			p.warnf(s.execLine, "ExecStart is ignored for Type=scm")
		}
	default:
		if !s.execSet || (strings.TrimSpace(s.execRaw) == "" && len(s.execArgs) == 0) {
			p.errorf(s.execLine, "ExecStart is required")
		} else {
			if p.unit.FormatVersion != 2 && len(s.execArgs) > 0 && execStartArgFootgun(s.execRaw, p.stat) {
				p.errorf(s.execLine, "ExecStart must be an executable path when ExecStartArg is set; unquoted arguments belong in ExecStartArg")
			}
			argv, err := buildArgv(s.execRaw, s.execArgs)
			if err != nil {
				p.errorf(s.execLine, "%s", err.Error())
			} else {
				spec.ExecStart = argv
				if !WindowsAbs(argv[0]) {
					p.errorf(s.execLine, "ExecStart must be an absolute path (SearchPath=no)")
				}
			}
		}

		if !s.wdSet || strings.TrimSpace(s.wd) == "" {
			p.warnf(s.wdLine, "WorkingDirectory is omitted; winunitd will not default to System32")
		} else {
			spec.WorkingDirectory = s.wd
			if !WindowsAbs(s.wd) {
				p.errorf(s.wdLine, "WorkingDirectory must be an absolute path")
			}
		}
	}

	spec.Environment = s.env
	if s.execStopSet || len(s.execStopArgs) > 0 {
		if spec.Type.IsExternalProxy() {
			p.errorf(s.execStopLine, "ExecStop is not valid for Type=%s", spec.Type)
		} else if !s.execStopSet || strings.TrimSpace(s.execStopRaw) == "" {
			p.errorf(s.execStopLine, "ExecStop requires an executable path")
		} else {
			if p.unit.FormatVersion != 2 && len(s.execStopArgs) > 0 && execStartArgFootgun(s.execStopRaw, p.stat) {
				p.errorf(s.execStopLine, "ExecStop must be an executable path when ExecStopArg is set")
			}
			argv, err := buildArgv(s.execStopRaw, s.execStopArgs)
			if err != nil {
				p.errorf(s.execStopLine, "invalid ExecStop: %s", err)
			} else {
				spec.ExecStop = argv
				if !WindowsAbs(argv[0]) {
					p.errorf(s.execStopLine, "ExecStop must be an absolute path (SearchPath=no)")
				}
			}
		}
	}

	rest := strings.ToLower(strings.TrimSpace(s.restart))
	if rest == "" {
		rest = string(RestartNo)
	}
	switch RestartPolicy(rest) {
	case RestartNo, RestartAlways, RestartOnFailure, RestartOnWatchdog:
		spec.Restart = RestartPolicy(rest)
	default:
		line := s.restLine
		p.errorf(line, "invalid Restart %q (supported: no, always, on-failure, on-watchdog)", s.restart)
	}

	if s.restartSec != "" {
		d, err := ParseDuration(s.restartSec)
		if err != nil {
			p.errorf(s.restartSecL, "invalid RestartSec: %s", err.Error())
		} else {
			spec.RestartSec = d
			spec.RestartSecSet = true
		}
	}
	p.finishRestartBackoff(spec, s)
	if s.timeoutStart != "" {
		d, err := ParseDuration(s.timeoutStart)
		if err != nil {
			p.errorf(s.timeoutStartL, "invalid TimeoutStartSec: %s", err.Error())
		} else {
			spec.TimeoutStartSec = d
			spec.TimeoutStartSecSet = true
		}
	}
	if s.timeoutStop != "" {
		d, err := ParseDuration(s.timeoutStop)
		if err != nil {
			p.errorf(s.timeoutStopL, "invalid TimeoutStopSec: %s", err.Error())
		} else {
			spec.TimeoutStopSec = d
			spec.TimeoutStopSecSet = true
		}
	}

	p.finishJobLimits(spec, s)
	if len(spec.ExecStop) > 0 && spec.TimeoutStopSecSet && spec.TimeoutStopSec <= 0 {
		p.errorf(s.timeoutStopL, "ExecStop requires a positive TimeoutStopSec")
	}

	if spec.Type.IsExternalProxy() {
		p.finishWatchdogPolicy(spec, s)
		tag := string(spec.Type)
		if s.notifyAccess != "" {
			p.warnf(s.notifyAccessL, "NotifyAccess is ignored for Type=%s", tag)
		}
		if s.watchdogSec != "" {
			p.warnf(s.watchdogSecL, "WatchdogSec is ignored for Type=%s", tag)
		}
		if s.watchdogMode != "" {
			p.warnf(s.watchdogModeL, "WatchdogMode is ignored for Type=%s", tag)
		}
		return
	}

	access := strings.ToLower(strings.TrimSpace(s.notifyAccess))
	if access == "" {
		if spec.Type == TypeNotify {
			spec.NotifyAccess = NotifyAccessMain
		}
	} else if NotifyAccess(access) == NotifyAccessMain {
		spec.NotifyAccess = NotifyAccessMain
	} else {
		p.errorf(s.notifyAccessL, "invalid NotifyAccess %q (supported: main)", s.notifyAccess)
	}

	if s.watchdogSec != "" {
		d, err := ParseDuration(s.watchdogSec)
		if err != nil {
			p.errorf(s.watchdogSecL, "invalid WatchdogSec: %s", err.Error())
		} else {
			spec.WatchdogSec = d
			spec.WatchdogSecSet = true
		}
	}

	p.finishWatchdog(spec, s)
	p.finishWatchdogPolicy(spec, s)
}

func (p *parser) finishJobLimits(spec *ServiceSpec, s *serviceBuilder) {
	if s.memoryMax != "" {
		n, err := parseMemoryMax(s.memoryMax)
		if err != nil {
			p.errorf(s.memoryMaxL, "%s", err.Error())
		} else {
			spec.MemoryMax = n
			spec.MemoryMaxSet = true
		}
	}
	if s.processLimit != "" {
		n, err := parseProcessLimit(s.processLimit)
		if err != nil {
			p.errorf(s.processLimitL, "%s", err.Error())
		} else {
			spec.ProcessLimit = n
			spec.ProcessLimitSet = true
		}
	}
	if s.priorityClass != "" {
		pc, err := parsePriorityClass(s.priorityClass)
		if err != nil {
			p.errorf(s.priorityClassL, "%s", err.Error())
		} else {
			spec.PriorityClass = pc
			spec.PriorityClassSet = true
		}
	}
	p.finishWindowsCPU(spec, s)
	if s.cpuWeight != "" {
		n, err := parseCPUWeight(s.cpuWeight)
		if err != nil {
			p.errorf(s.cpuWeightL, "%s", err.Error())
		} else {
			spec.CPUWeight = n
			spec.CPUWeightSet = true
		}
	}
	if s.cpuQuota != "" {
		n, err := parseCPUQuota(s.cpuQuota)
		if err != nil {
			p.errorf(s.cpuQuotaL, "%s", err.Error())
		} else {
			spec.CPUQuota = n
			spec.CPUQuotaSet = true
		}
	}
	if spec.CPUWeightSet && spec.CPUQuotaSet {
		line := s.cpuQuotaL
		if s.cpuWeightL > 0 && (line == 0 || s.cpuWeightL < line) {
			line = s.cpuWeightL
		}
		p.errorf(line, "CPUWeight and CPUQuota cannot both be set")
	}
	if s.ioPriority != "" {
		ip, err := parseIoPriority(s.ioPriority)
		if err != nil {
			p.errorf(s.ioPriorityL, "%s", err.Error())
		} else {
			spec.IoPriority = ip
			spec.IoPrioritySet = true
		}
	}
	if !spec.Type.IsExternalProxy() {
		return
	}
	tag := string(spec.Type)
	if spec.MemoryMaxSet {
		p.warnf(s.memoryMaxL, "MemoryMax is ignored for Type=%s", tag)
		spec.MemoryMax = 0
		spec.MemoryMaxSet = false
	}
	if spec.ProcessLimitSet {
		p.warnf(s.processLimitL, "ProcessLimit is ignored for Type=%s", tag)
		spec.ProcessLimit = 0
		spec.ProcessLimitSet = false
	}
	if spec.PriorityClassSet {
		p.warnf(s.priorityClassL, "PriorityClass is ignored for Type=%s", tag)
		spec.PriorityClass = ""
		spec.PriorityClassSet = false
	}
	if spec.CPUWeightSet {
		p.warnf(s.cpuWeightL, "CPUWeight is ignored for Type=%s", tag)
		spec.CPUWeight = 0
		spec.CPUWeightSet = false
	}
	if spec.CPUQuotaSet {
		p.warnf(s.cpuQuotaL, "CPUQuota is ignored for Type=%s", tag)
		spec.CPUQuota = 0
		spec.CPUQuotaSet = false
	}
	if spec.IoPrioritySet {
		p.warnf(s.ioPriorityL, "IoPriority is ignored for Type=%s", tag)
		spec.IoPriority = ""
		spec.IoPrioritySet = false
	}
}

func (p *parser) finishTimer() {
	spec := &TimerSpec{}
	p.unit.Timer = spec
	t := p.timer
	if t == nil {
		p.errorf(0, "timer unit requires a [Timer] section")
		p.defaultTimerUnit(spec)
		return
	}

	parseTimerDuration := func(raw string, line int, label string) (time.Duration, bool) {
		if raw == "" {
			return 0, false
		}
		d, err := ParseDuration(raw)
		if err != nil {
			p.errorf(line, "invalid %s: %s", label, err.Error())
			return 0, false
		}
		return d, true
	}
	if d, ok := parseTimerDuration(t.onBoot, t.onBootL, "OnBootSec"); ok {
		spec.OnBootSec = d
		spec.OnBootSecSet = true
	}
	if d, ok := parseTimerDuration(t.onStartup, t.onStartupL, "OnStartupSec"); ok {
		spec.OnStartupSec = d
		spec.OnStartupSecSet = true
	}
	if d, ok := parseTimerDuration(t.onUnitActive, t.onUnitActiveL, "OnUnitActiveSec"); ok {
		spec.OnUnitActiveSec = d
		spec.OnUnitActiveSecSet = true
	}

	for i, expr := range t.calendars {
		line := t.calendarLines[i]
		if i >= timers.MaxCalendarExpressions {
			p.errorf(line, "timer exceeds %d OnCalendar expressions", timers.MaxCalendarExpressions)
			break
		}
		if strings.TrimSpace(expr) == "" {
			p.errorf(line, "invalid OnCalendar: empty calendar expression")
			continue
		}
		cal, err := timers.ParseCalendar(expr)
		if err != nil {
			p.errorf(line, "invalid OnCalendar: %s", err.Error())
			continue
		}
		spec.OnCalendar = append(spec.OnCalendar, cal)
	}

	if t.persistentSet {
		b, err := parseBool(t.persistent)
		if err != nil {
			p.errorf(t.persistentL, "invalid Persistent: %s", err.Error())
		} else {
			spec.Persistent = b
		}
	}

	if t.unitSet {
		name := NormalizeName(t.unit)
		if name == "" {
			p.errorf(t.unitL, "Unit= is empty")
		} else {
			spec.Unit = name
		}
	} else {
		p.defaultTimerUnit(spec)
	}

	hasTrigger := t.onBoot != "" || t.onStartup != "" || t.onUnitActive != "" || len(t.calendars) > 0
	if !hasTrigger {
		p.errorf(0, "timer must specify at least one of OnBootSec, OnStartupSec, OnUnitActiveSec, OnCalendar")
	}
}

func (p *parser) defaultTimerUnit(spec *TimerSpec) {
	spec.Unit = CompanionService(p.name)
}

func (p *parser) finishRegistry() {
	spec := &RegistrySpec{Unit: CompanionService(p.name)}
	p.unit.Registry = spec
	r := p.reg
	if r == nil {
		p.errorf(0, "registry unit requires a [Registry] section")
		return
	}
	for i, raw := range r.changed {
		line := r.lines[i]
		if strings.TrimSpace(raw) == "" {
			p.errorf(line, "empty registry path")
			continue
		}
		key, err := registry.ParseKey(raw)
		if err != nil {
			p.errorf(line, "invalid RegistryChanged: %s", err.Error())
			continue
		}
		spec.Changed = append(spec.Changed, key)
	}
	if len(r.changed) == 0 {
		p.errorf(0, "registry must specify RegistryChanged")
	}
}

func (p *parser) finishEventLog() {
	spec := &EventLogSpec{Unit: CompanionService(p.name)}
	p.unit.EventLog = spec
	ev := p.evt
	if ev == nil {
		p.errorf(0, "eventlog unit requires a [EventLog] section")
		return
	}
	for i, raw := range ev.triggers {
		line := ev.lines[i]
		if strings.TrimSpace(raw) == "" {
			p.errorf(line, "empty EventLogTrigger")
			continue
		}
		tr, err := eventlog.ParseTrigger(raw)
		if err != nil {
			p.errorf(line, "invalid EventLogTrigger: %s", err.Error())
			continue
		}
		spec.Triggers = append(spec.Triggers, tr)
	}
	if len(ev.triggers) == 0 {
		p.errorf(0, "eventlog must specify EventLogTrigger")
	}
}

func (p *parser) finishPath() {
	spec := &PathSpec{Unit: CompanionService(p.name)}
	p.unit.PathWatch = spec
	ph := p.pth
	if ph == nil {
		p.errorf(0, "path unit requires a [Path] section")
		return
	}
	if ph.existsAll && p.unit.FormatVersion != 2 {
		p.errorf(0, "PathExistsAll requires FormatVersion=2")
	}
	if ph.existsAll && ph.existsAny {
		p.errorf(0, "PathExists and PathExistsAll cannot be combined")
	}
	spec.ExistsAny = p.unit.FormatVersion == 2 && !ph.existsAll
	for i, raw := range ph.changed {
		line := ph.changedLines[i]
		if strings.TrimSpace(raw) == "" {
			p.errorf(line, "empty path")
			continue
		}
		sp, err := pathwatch.Parse(raw)
		if err != nil {
			p.errorf(line, "invalid PathChanged: %s", err.Error())
			continue
		}
		spec.Changed = append(spec.Changed, sp)
	}
	for i, raw := range ph.exists {
		line := ph.existsLines[i]
		if strings.TrimSpace(raw) == "" {
			p.errorf(line, "empty path")
			continue
		}
		sp, err := pathwatch.ParseExists(raw)
		if err != nil {
			p.errorf(line, "invalid PathExists: %s", err.Error())
			continue
		}
		spec.Exists = append(spec.Exists, sp)
	}
	if len(ph.changed) == 0 && len(ph.exists) == 0 {
		p.errorf(0, "path must specify PathChanged or PathExists")
	}
}
