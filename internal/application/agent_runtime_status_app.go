package application

import (
	"context"
	"slices"

	"github.com/quant4dad/internal/agentbridge"
)

type AgentProfileStatus struct {
	ID    string             `json:"id"`
	Tools []ToolAvailability `json:"tools"`
}
type AgentRuntimeStatus struct {
	Status          string                        `json:"status"`
	Profiles        []AgentProfileStatus          `json:"profiles"`
	Runtime         *AgentRuntimeVersionStatus    `json:"runtime,omitempty"`
	ProfileSource   string                        `json:"profile_source,omitempty"`
	ProfilesAligned *bool                         `json:"profiles_aligned,omitempty"`
	RuntimeProfiles []agentbridge.ProfileIdentity `json:"runtime_profiles,omitempty"`
}
type AgentRuntimeVersionStatus struct {
	Version        string `json:"version"`
	AdapterVersion string `json:"adapter_version"`
	DSHVersion     string `json:"dsh_version"`
	ImageDigest    string `json:"image_digest"`
	BridgeProtocol int    `json:"bridge_protocol"`
}

func (a *AgentRunApplication) RuntimeStatus(ctx context.Context) AgentRuntimeStatus {
	out := AgentRuntimeStatus{Status: "unavailable", Profiles: []AgentProfileStatus{}}
	for _, id := range a.Profiles() {
		profile := AgentProfileStatus{ID: id, Tools: []ToolAvailability{}}
		for _, name := range P0ProfileTools(id) {
			tool := ToolAvailability{Name: name, Available: slices.Contains(a.tools[id], name)}
			if !tool.Available {
				tool.Reason = "business_adapter_unavailable"
				if a.profiles[id].Budgets.MaxToolCalls == 0 {
					tool.Reason = "profile_tools_disabled"
				}
			}
			profile.Tools = append(profile.Tools, tool)
		}
		out.Profiles = append(out.Profiles, profile)
	}
	caps, err := a.runtime.Capabilities(ctx)
	if err != nil {
		return out
	}
	out.Status = "incompatible"
	out.ProfileSource, out.ProfilesAligned, out.RuntimeProfiles = caps.ProfileSource, caps.ProfilesAligned, caps.Profiles
	if caps.ProfilesAligned != nil {
		aligned := *caps.ProfilesAligned
		for id, p := range a.profiles {
			found := false
			for _, live := range caps.Profiles {
				if live.ID == id {
					found = live.Revision == a.revisions[id] && live.PromptBundleDigest == p.PromptBundleDigest && live.SkillsDigest == p.SkillsDigest && live.ToolCatalogRevision == p.ToolCatalogRevision
				}
			}
			aligned = aligned && found
		}
		out.ProfilesAligned = &aligned
		if !aligned {
			out.Status = "configuration_mismatch"
			return out
		}
	}
	manifest, err := a.currentManifest(ctx)
	if err != nil {
		out.Status = "unavailable"
		return out
	}
	out.Runtime = &AgentRuntimeVersionStatus{Version: manifest.AgentRuntimeVersion, AdapterVersion: manifest.AdapterVersion,
		DSHVersion: manifest.DSHVersion, ImageDigest: manifest.AgentImageDigest, BridgeProtocol: caps.BridgeProtocol}
	if live := caps.RuntimeManifest; live != nil {
		if live.AgentImageDigest != manifest.AgentImageDigest || live.AgentRuntimeVersion != manifest.AgentRuntimeVersion || live.Q4DVersion != manifest.Q4DVersion {
			return out
		}
	} else if a.releaseRequired {
		return out
	}
	if caps.BridgeProtocol != 1 || caps.AdapterVersion != manifest.AdapterVersion || caps.DSHVersion != manifest.DSHVersion || !caps.Features.SessionResume || !caps.Features.EventReplay || !caps.Features.Cancel {
		return out
	}
	if caps.Backend != "cordis" || caps.SessionFormat != 0 || caps.EventJournalFormat != 1 || caps.SessionBindingFormat == nil || *caps.SessionBindingFormat != 1 || !caps.Features.SessionProvisioning {
		return out
	}
	for _, tools := range a.tools {
		for _, write := range []string{"create_strategy", "update_strategy", "create_pipeline", "update_pipeline", "set_pipeline_status"} {
			if slices.Contains(tools, write) && !caps.Features.Approval {
				return out
			}
		}
	}
	model, err := a.settings.GetAgentModelCatalog(ctx)
	if err != nil {
		out.Status = "unavailable"
		return out
	}
	if caps.ModelConfigRevision != model.Revision {
		out.Status = "synchronizing"
		return out
	}
	out.Status = "ready"
	if caps.ReadyModels != nil && len(*caps.ReadyModels) == 0 {
		out.Status = "model_check_required"
	}
	return out
}
