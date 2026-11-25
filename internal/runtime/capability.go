package runtime

import "strings"

// CapabilityProfile is the task-facing capability vocabulary. It is distinct
// from the image profile (core/full/web): a binary-analysis task needs the full
// image, but the platform also needs to say which named tools the task relies
// on and whether the current image actually contains them.
type CapabilityProfile string

const (
	CapabilityWeb      CapabilityProfile = "web"
	CapabilityBinary   CapabilityProfile = "binary"
	CapabilityFirmware CapabilityProfile = "firmware"
	CapabilityIdentity CapabilityProfile = "identity"
	CapabilityCloud    CapabilityProfile = "cloud"
)

// CapabilitySpec declares the image/network posture and the named tools one
// capability profile expects. Requirements are advisory to the planner and
// authoritative to the capability report; they never widen authorization.
type CapabilitySpec struct {
	Profile      CapabilityProfile `json:"profile"`
	ImageProfile string            `json:"image_profile"`
	NetProfile   string            `json:"net_profile"`
	Required     []string          `json:"required_tools"`
	Description  string            `json:"description"`
}

var capabilitySpecs = []CapabilitySpec{
	{
		Profile: CapabilityWeb, ImageProfile: ProfileWeb, NetProfile: NetProfileProxyOnly,
		Required:    []string{"subfinder", "httpx", "nuclei", "ffuf", "sqlmap"},
		Description: "Web application discovery, fuzzing and exploitation baseline.",
	},
	{
		Profile: CapabilityBinary, ImageProfile: ProfileFull, NetProfile: NetProfileProxyOnly,
		Required:    []string{"gdb", "strace", "rg", "python3"},
		Description: "Native binary analysis: debugger, tracing, search and scripting.",
	},
	{
		Profile: CapabilityFirmware, ImageProfile: ProfileFull, NetProfile: NetProfileProxyOnly,
		Required:    []string{"gdb", "rg", "python3", "jq"},
		Description: "Firmware/embedded image inspection and structured extraction.",
	},
	{
		Profile: CapabilityIdentity, ImageProfile: ProfileFull, NetProfile: NetProfileProxyOnly,
		Required:    []string{"hydra", "john", "hashcat", "impacket-secretsdump"},
		Description: "Credential attacks, hash cracking and identity/AD collection.",
	},
	{
		Profile: CapabilityCloud, ImageProfile: ProfileFull, NetProfile: NetProfileProxyOnly,
		Required:    []string{"curl", "python3", "jq"},
		Description: "Cloud/API control-plane work using HTTP and scripting primitives.",
	},
}

// CapabilityProfiles returns a copy of the declared capability vocabulary.
func CapabilityProfiles() []CapabilitySpec {
	out := make([]CapabilitySpec, len(capabilitySpecs))
	copy(out, capabilitySpecs)
	for i := range out {
		out[i].Required = append([]string(nil), capabilitySpecs[i].Required...)
	}
	return out
}

// CapabilityFor normalizes a task-supplied name. Unknown values map to web so
// the report stays useful without inventing a capability.
func CapabilityFor(raw string) CapabilitySpec {
	name := CapabilityProfile(strings.ToLower(strings.TrimSpace(raw)))
	for _, spec := range capabilitySpecs {
		if spec.Profile == name {
			return spec
		}
	}
	return capabilitySpecs[0]
}

// CapabilityReport is the deterministic availability view over one live
// toolpack manifest. Status=capability_missing means "the image does not
// contain a tool this profile declares", not "the model chose a bad action".
type CapabilityReport struct {
	Profile      CapabilityProfile `json:"profile"`
	ImageProfile string            `json:"image_profile"`
	NetProfile   string            `json:"net_profile"`
	Required     []string          `json:"required_tools"`
	Available    []string          `json:"available_tools,omitempty"`
	Missing      []string          `json:"missing_tools,omitempty"`
	Status       string            `json:"status"`
}

// HasTool reports whether the manifest declares a named tool.
func (p Toolpack) HasTool(name string) bool {
	name = strings.TrimSpace(name)
	for _, tool := range p.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

// CapabilityReports reports every declared profile against this manifest.
func (p Toolpack) CapabilityReports() []CapabilityReport {
	out := make([]CapabilityReport, 0, len(capabilitySpecs))
	for _, spec := range capabilitySpecs {
		out = append(out, p.CapabilityReport(spec.Profile))
	}
	return out
}

// CapabilityReport reports one profile against this manifest.
func (p Toolpack) CapabilityReport(profile CapabilityProfile) CapabilityReport {
	spec := CapabilityFor(string(profile))
	report := CapabilityReport{
		Profile: spec.Profile, ImageProfile: spec.ImageProfile, NetProfile: spec.NetProfile,
		Required: append([]string(nil), spec.Required...), Status: "available",
	}
	for _, name := range spec.Required {
		if p.HasTool(name) {
			report.Available = append(report.Available, name)
		} else {
			report.Missing = append(report.Missing, name)
		}
	}
	if len(report.Missing) > 0 {
		report.Status = "capability_missing"
	}
	return report
}
