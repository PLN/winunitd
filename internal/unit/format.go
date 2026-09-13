package unit

import (
	"strconv"
)

func (p *parser) finishWindowsCPU(spec *ServiceSpec, s *serviceBuilder) {
	if p.unit.FormatVersion != 2 {
		if s.windowsCPUWeightL != 0 {
			p.errorf(s.windowsCPUWeightL, "WindowsCPUWeight requires FormatVersion=2")
		}
		if s.windowsCPUQuotaL != 0 {
			p.errorf(s.windowsCPUQuotaL, "WindowsCPUQuota requires FormatVersion=2")
		}
		return
	}
	if s.cpuWeightL != 0 {
		p.errorf(s.cpuWeightL, "CPUWeight is a legacy directive; convert to WindowsCPUWeight for FormatVersion=2")
	}
	if s.cpuQuotaL != 0 {
		p.errorf(s.cpuQuotaL, "CPUQuota is a legacy directive; convert to WindowsCPUQuota for FormatVersion=2")
	}
	if s.windowsCPUWeightL != 0 {
		n, err := strconv.ParseUint(s.windowsCPUWeight, 10, 32)
		if err != nil || n < 1 || n > 9 {
			p.errorf(s.windowsCPUWeightL, "WindowsCPUWeight must be an integer from 1 to 9")
		} else {
			spec.WindowsCPUWeight = uint32(n)
		}
	}
	if s.windowsCPUQuotaL != 0 {
		n, err := parseCPUQuota(s.windowsCPUQuota)
		if err != nil {
			p.errorf(s.windowsCPUQuotaL, "WindowsCPUQuota must be an integer percentage from 1%% to 100%%")
		} else {
			spec.WindowsCPUQuota = n
		}
	}
	if s.windowsCPUWeightL != 0 && s.windowsCPUQuotaL != 0 {
		p.errorf(s.windowsCPUQuotaL, "WindowsCPUWeight and WindowsCPUQuota cannot both be set")
	}
	if spec.Type.IsExternalProxy() && (s.windowsCPUWeightL != 0 || s.windowsCPUQuotaL != 0) {
		p.errorf(0, "Windows CPU controls require a managed process; Type=%s has external ownership", spec.Type)
	}
}
