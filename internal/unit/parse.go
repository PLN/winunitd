package unit

import (
	"fmt"
	"strings"
	"time"

	"github.com/PLN/winunitd/internal/registry"
	"github.com/PLN/winunitd/internal/timers"
)

var knownDirectives = map[string]map[string]bool{
	"Unit": {
		"Description":                true,
		"Requires":                   true,
		"Wants":                      true,
		"BindsTo":                    true,
		"PartOf":                     true,
		"After":                      true,
		"Before":                     true,
		"RequiresInteractiveSession": true,
	},
	"Service": {
		"Type":                   true,
		"ServiceName":            true,
		"ExecStart":              true,
		"ExecStartArg":           true,
		"WorkingDirectory":       true,
		"Environment":            true,
		"Restart":                true,
		"RestartSec":             true,
		"TimeoutStartSec":        true,
		"TimeoutStopSec":         true,
		"NotifyAccess":           true,
		"WatchdogSec":            true,
		"WatchdogMode":           true,
		"WatchdogEndpoint":       true,
		"WatchdogExpectedStatus": true,
		"MemoryMax":              true,
		"ProcessLimit":           true,
		"PriorityClass":          true,
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
	"Install": {
		"WantedBy": true,
	},
}

var sectionsByKind = map[Kind]map[string]bool{
	KindService:  {"Unit": true, "Service": true, "Install": true},
	KindTimer:    {"Unit": true, "Timer": true, "Install": true},
	KindTarget:   {"Unit": true, "Install": true},
	KindRegistry: {"Unit": true, "Registry": true, "Install": true},
}

type serviceBuilder struct {
	typ      string
	typLine  int
	execRaw  string
	execLine int
	execSet  bool
	execArgs []string
	wd       string
	wdLine   int
	wdSet    bool
	env      []EnvVar
	restart  string
	restLine int
	restSet  bool

	restartSec    string
	restartSecL   int
	timeoutStart  string
	timeoutStartL int
	timeoutStop   string
	timeoutStopL  int

	serviceName  string
	serviceNameL int

	notifyAccess  string
	notifyAccessL int
	watchdogSec   string
	watchdogSecL  int
	watchdogMode  string
	watchdogModeL int
	watchdogEP    string
	watchdogEPL   int
	watchdogStat  string
	watchdogStatL int

	memoryMax      string
	memoryMaxL     int
	processLimit   string
	processLimitL  int
	priorityClass  string
	priorityClassL int
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
	unitSet       bool
}

type registryBuilder struct {
	changed []string
	lines   []int
}

type parser struct {
	path   string
	name   string
	kind   Kind
	unit   *Unit
	issues []Issue

	svc   *serviceBuilder
	timer *timerBuilder
	reg   *registryBuilder

	ris     string
	risLine int
	risSet  bool
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
// name is the unit file name (foo.service); path is used in diagnostics.
func Parse(path, name string, src []byte) Report {
	kind, err := KindFromName(name)
	if err != nil {
		return Report{Issues: []Issue{{
			Path:     path,
			Severity: SeverityError,
			Message:  err.Error(),
		}}}
	}

	p := &parser{
		path: path,
		name: name,
		kind: kind,
		unit: &Unit{
			Name: name,
			Path: path,
			Kind: kind,
		},
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
	case "Install":
		p.applyInstall(e)
	}
}

func (p *parser) applyUnit(e iniEntry) {
	switch e.key {
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
	}
}

func (p *parser) applyService(e iniEntry) {
	s := p.svc
	switch e.key {
	case "Type":
		s.typ = e.value
		s.typLine = e.line
	case "ServiceName":
		s.serviceName = e.value
		s.serviceNameL = e.line
	case "ExecStart":
		s.execRaw = e.value
		s.execLine = e.line
		s.execSet = true
	case "ExecStartArg":
		s.execArgs = append(s.execArgs, e.value)
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
	return append(cur, parseUnitNames(value)...)
}

func (p *parser) finish() {
	switch p.kind {
	case KindService:
		p.finishService()
	case KindTimer:
		p.finishTimer()
	case KindRegistry:
		p.finishRegistry()
	case KindTarget:
		// targets have no extra required fields
	}
	p.finishInteractiveSession()
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
		p.warnf(0, "WorkingDirectory is omitted; winunitd will not default to System32")
		return
	}

	typ := strings.ToLower(strings.TrimSpace(s.typ))
	if typ == "" {
		typ = string(TypeSimple)
	}
	switch ServiceType(typ) {
	case TypeSimple, TypeOneshot, TypeNotify, TypeSCM:
		spec.Type = ServiceType(typ)
	default:
		p.errorf(s.typLine, "invalid Type %q (supported: simple, oneshot, notify, scm)", s.typ)
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

	if spec.Type != TypeSCM {
		if !s.execSet || (strings.TrimSpace(s.execRaw) == "" && len(s.execArgs) == 0) {
			p.errorf(s.execLine, "ExecStart is required")
		} else {
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
	} else if s.execSet && (strings.TrimSpace(s.execRaw) != "" || len(s.execArgs) > 0) {
		p.warnf(s.execLine, "ExecStart is ignored for Type=scm")
	}

	spec.Environment = s.env

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
		d, err := parseDuration(s.restartSec)
		if err != nil {
			p.errorf(s.restartSecL, "invalid RestartSec: %s", err.Error())
		} else {
			spec.RestartSec = d
			spec.RestartSecSet = true
		}
	}
	if s.timeoutStart != "" {
		d, err := parseDuration(s.timeoutStart)
		if err != nil {
			p.errorf(s.timeoutStartL, "invalid TimeoutStartSec: %s", err.Error())
		} else {
			spec.TimeoutStartSec = d
			spec.TimeoutStartSecSet = true
		}
	}
	if s.timeoutStop != "" {
		d, err := parseDuration(s.timeoutStop)
		if err != nil {
			p.errorf(s.timeoutStopL, "invalid TimeoutStopSec: %s", err.Error())
		} else {
			spec.TimeoutStopSec = d
			spec.TimeoutStopSecSet = true
		}
	}

	p.finishJobLimits(spec, s)

	if spec.Type == TypeSCM {
		if s.notifyAccess != "" {
			p.warnf(s.notifyAccessL, "NotifyAccess is ignored for Type=scm")
		}
		if s.watchdogSec != "" {
			p.warnf(s.watchdogSecL, "WatchdogSec is ignored for Type=scm")
		}
		if s.watchdogMode != "" {
			p.warnf(s.watchdogModeL, "WatchdogMode is ignored for Type=scm")
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
		d, err := parseDuration(s.watchdogSec)
		if err != nil {
			p.errorf(s.watchdogSecL, "invalid WatchdogSec: %s", err.Error())
		} else {
			spec.WatchdogSec = d
			spec.WatchdogSecSet = true
		}
	}

	p.finishWatchdog(spec, s)
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
	if spec.Type != TypeSCM {
		return
	}
	if spec.MemoryMaxSet {
		p.warnf(s.memoryMaxL, "MemoryMax is ignored for Type=scm")
		spec.MemoryMax = 0
		spec.MemoryMaxSet = false
	}
	if spec.ProcessLimitSet {
		p.warnf(s.processLimitL, "ProcessLimit is ignored for Type=scm")
		spec.ProcessLimit = 0
		spec.ProcessLimitSet = false
	}
	if spec.PriorityClassSet {
		p.warnf(s.priorityClassL, "PriorityClass is ignored for Type=scm")
		spec.PriorityClass = ""
		spec.PriorityClassSet = false
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
		d, err := parseDuration(raw)
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
		name := strings.TrimSpace(t.unit)
		if name == "" {
			p.errorf(0, "Unit= is empty")
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
