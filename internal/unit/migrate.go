package unit

import (
	"bytes"
	"fmt"
	"strings"
)

// Migration is a validated preview. Conversion never writes the source file.
type Migration struct {
	SourceVersion int
	TargetVersion int
	Changes       []string
	Output        []byte
	Issues        []Issue
}

// ConvertToV2 preserves effective beta path predicates and Windows CPU limits.
// Comments and unrelated directives remain in order; output uses UTF-8/LF.
func ConvertToV2(path, name string, src []byte) Migration {
	m := Migration{TargetVersion: 2}
	fail := func(message string) Migration {
		m.Output = nil
		m.Issues = append(m.Issues, Issue{Path: path, Severity: SeverityError, Message: message})
		return m
	}
	if len(src) > MaxFileBytes {
		return fail(fmt.Sprintf("unit file exceeds %d bytes", MaxFileBytes))
	}
	report := Parse(path, name, src)
	m.Issues = report.Issues
	if report.HasError() {
		return m
	}
	m.SourceVersion = report.Unit.FormatVersion
	if m.SourceVersion == 2 {
		m.Output = bytes.Clone(src)
		return m
	}
	doc, _ := parseINI(src, path)
	lines := splitLines(string(stripBOM(src)))
	replacements := make(map[int]string)
	marker := false
	var weight, quota *iniEntry
	for _, sec := range doc.sections {
		for _, e := range sec.entries {
			switch {
			case sec.name == "Unit" && e.key == "FormatVersion":
				marker = true
				replacements[e.line-1] = "FormatVersion=2"
			case sec.name == "Path" && e.key == "PathExists":
				replacements[e.line-1] = "PathExistsAll=" + e.value
			case sec.name == "Service" && e.key == "CPUWeight":
				copy := e
				weight = &copy
				replacements[e.line-1] = ""
			case sec.name == "Service" && e.key == "CPUQuota":
				copy := e
				quota = &copy
				replacements[e.line-1] = ""
			}
		}
	}
	m.Changes = append(m.Changes, "select FormatVersion=2 explicitly")
	if pathSpec := report.Unit.PathWatch; pathSpec != nil && len(pathSpec.Exists) > 0 {
		m.Changes = append(m.Changes, "rename PathExists to PathExistsAll to preserve the existing AND predicate")
	}
	if weight != nil {
		if svc := report.Unit.Service; svc != nil && svc.CPUWeightSet {
			n := WindowsCPUWeight(svc.CPUWeight)
			replacements[weight.line-1] = fmt.Sprintf("WindowsCPUWeight=%d", n)
			m.Changes = append(m.Changes, fmt.Sprintf("CPUWeight=%d becomes WindowsCPUWeight=%d; effective native weight is unchanged", svc.CPUWeight, n))
		} else {
			m.Changes = append(m.Changes, "remove CPUWeight assignments that have no effective resource policy")
		}
	}
	if quota != nil {
		if svc := report.Unit.Service; svc != nil && svc.CPUQuotaSet {
			replacements[quota.line-1] = fmt.Sprintf("WindowsCPUQuota=%d%%", svc.CPUQuota)
			m.Changes = append(m.Changes, fmt.Sprintf("CPUQuota=%d%% becomes WindowsCPUQuota=%d%%; effective native quota is unchanged", svc.CPUQuota, svc.CPUQuota))
		} else {
			m.Changes = append(m.Changes, "remove CPUQuota assignments that have no effective resource policy")
		}
	}
	var output strings.Builder
	if !marker {
		output.WriteString("[Unit]\nFormatVersion=2\n\n")
	}
	for i := 0; i < len(lines); i++ {
		if replacement, ok := replacements[i]; ok {
			for isLineContinuation(lines[i]) && i+1 < len(lines) {
				i++
			}
			if replacement != "" {
				output.WriteString(replacement)
				output.WriteByte('\n')
			}
		} else {
			output.WriteString(lines[i])
			if i < len(lines)-1 {
				output.WriteByte('\n')
			}
		}
	}
	m.Output = []byte(output.String())
	if len(m.Output) > MaxFileBytes {
		return fail(fmt.Sprintf("converted unit exceeds %d bytes", MaxFileBytes))
	}
	converted := Parse(path, name, m.Output)
	if converted.HasError() {
		m.Output = nil
		m.Issues = append(m.Issues, converted.Issues...)
	}
	return m
}
