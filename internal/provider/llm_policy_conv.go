package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi"
	"github.com/ferentin-net/ferentin-cli-app/pkg/adminapi/gen"
)

// toCreateRequest builds an SDK Create body from the Terraform plan model.
func (m *LLMPolicyResourceModel) toCreateRequest(ctx context.Context) (adminapi.LLMPolicyCreate, diag.Diagnostics) {
	var diags diag.Diagnostics
	out := adminapi.LLMPolicyCreate{
		Name:     m.Name.ValueString(),
		Priority: 100, // sensible default — overridden below if user set it
	}
	if !m.Priority.IsNull() && !m.Priority.IsUnknown() {
		out.Priority = int32(m.Priority.ValueInt64())
	}

	setStringPtr(m.Description, &out.Description)
	setStringPtr(m.SystemPrompt, &out.SystemPrompt)
	setStringPtr(m.DeveloperPrompt, &out.DeveloperPrompt)
	setStringPtr(m.Message, &out.Message)
	setBoolPtr(m.Enabled, &out.Enabled)
	setBoolPtr(m.DisallowClientDeveloper, &out.DisallowClientDeveloper)
	setBoolPtr(m.DisallowClientSystem, &out.DisallowClientSystem)
	setBoolPtr(m.PromptCacheEnabled, &out.PromptCacheEnabled)
	setBoolPtr(m.AllowProviderStorage, &out.AllowProviderStorage)
	setBoolPtr(m.SummaryEnabled, &out.SummaryEnabled)
	setBoolPtr(m.UseGatewayPrompts, &out.UseGatewayPrompts)

	// provider_instances is a required non-pointer []string on the wire.
	// Send empty slice when the user didn't set any (rather than nil) so
	// the platform sees an explicit empty allowlist rather than "missing".
	out.ProviderInstances = []string{}
	if !m.ProviderInstances.IsNull() && !m.ProviderInstances.IsUnknown() {
		var instances []string
		d := m.ProviderInstances.ElementsAs(ctx, &instances, false)
		diags.Append(d...)
		if !d.HasError() {
			out.ProviderInstances = instances
		}
	}

	// limits
	if m.Limits != nil {
		out.Limits = m.Limits.toSDK()
	}

	// criteria — diagnostics are appended unconditionally: a criteria block that
	// failed to convert produces no entries, and swallowing the diag there would
	// silently send a policy with NO criteria (i.e. one that matches everyone).
	crits, d := criteriaListToSDK(m.Criteria)
	diags.Append(d...)
	if len(crits) > 0 {
		out.Criteria = &crits
	}

	return out, diags
}

// toUpdateRequest is the Update analog. Same shape as Create — both wire
// schemas have Name + Priority + ProviderInstances as required.
func (m *LLMPolicyResourceModel) toUpdateRequest(ctx context.Context) (adminapi.LLMPolicyUpdate, diag.Diagnostics) {
	var diags diag.Diagnostics
	out := adminapi.LLMPolicyUpdate{
		Name:     m.Name.ValueString(),
		Priority: 100,
	}
	if !m.Priority.IsNull() && !m.Priority.IsUnknown() {
		out.Priority = int32(m.Priority.ValueInt64())
	}

	setStringPtr(m.Description, &out.Description)
	setStringPtr(m.SystemPrompt, &out.SystemPrompt)
	setStringPtr(m.DeveloperPrompt, &out.DeveloperPrompt)
	setStringPtr(m.Message, &out.Message)
	setBoolPtr(m.Enabled, &out.Enabled)
	setBoolPtr(m.DisallowClientDeveloper, &out.DisallowClientDeveloper)
	setBoolPtr(m.DisallowClientSystem, &out.DisallowClientSystem)
	setBoolPtr(m.PromptCacheEnabled, &out.PromptCacheEnabled)
	setBoolPtr(m.AllowProviderStorage, &out.AllowProviderStorage)
	setBoolPtr(m.SummaryEnabled, &out.SummaryEnabled)
	setBoolPtr(m.UseGatewayPrompts, &out.UseGatewayPrompts)

	if !m.ProviderInstances.IsNull() && !m.ProviderInstances.IsUnknown() {
		var instances []string
		d := m.ProviderInstances.ElementsAs(ctx, &instances, false)
		diags.Append(d...)
		if !d.HasError() {
			out.ProviderInstances = instances
		}
	}
	if m.Limits != nil {
		out.Limits = m.Limits.toSDK()
	}
	crits, d := criteriaListToSDK(m.Criteria)
	diags.Append(d...)
	if len(crits) > 0 {
		out.Criteria = &crits
	}
	return out, diags
}

// LLMPolicyLimitsModel → SDK (gen.ModelSurfaceLimits).
func (l *LLMPolicyLimitsModel) toSDK() *gen.ModelSurfaceLimits {
	if l == nil {
		return nil
	}
	out := &gen.ModelSurfaceLimits{}
	if !l.MaxTokens.IsNull() && !l.MaxTokens.IsUnknown() {
		v := int32(l.MaxTokens.ValueInt64())
		out.MaxTokens = &v
	}
	if !l.MaxRequestKb.IsNull() && !l.MaxRequestKb.IsUnknown() {
		v := int32(l.MaxRequestKb.ValueInt64())
		out.MaxRequestKb = &v
	}
	if !l.MaxFilesPerRequest.IsNull() && !l.MaxFilesPerRequest.IsUnknown() {
		v := int32(l.MaxFilesPerRequest.ValueInt64())
		out.MaxFilesPerRequest = &v
	}
	if !l.MaxImagesPerRequest.IsNull() && !l.MaxImagesPerRequest.IsUnknown() {
		v := int32(l.MaxImagesPerRequest.ValueInt64())
		out.MaxImagesPerRequest = &v
	}
	if !l.MaxImageBytes.IsNull() && !l.MaxImageBytes.IsUnknown() {
		v := l.MaxImageBytes.ValueInt64()
		out.MaxImageBytes = &v
	}
	if !l.MaxAudioBytes.IsNull() && !l.MaxAudioBytes.IsUnknown() {
		v := l.MaxAudioBytes.ValueInt64()
		out.MaxAudioBytes = &v
	}
	if !l.MaxAudioDurationSec.IsNull() && !l.MaxAudioDurationSec.IsUnknown() {
		v := int32(l.MaxAudioDurationSec.ValueInt64())
		out.MaxAudioDurationSec = &v
	}
	if !l.MaxToolArgumentsBytes.IsNull() && !l.MaxToolArgumentsBytes.IsUnknown() {
		v := int32(l.MaxToolArgumentsBytes.ValueInt64())
		out.MaxToolArgumentsBytes = &v
	}
	if !l.RequestTimeoutMs.IsNull() && !l.RequestTimeoutMs.IsUnknown() {
		v := int32(l.RequestTimeoutMs.ValueInt64())
		out.RequestTimeoutMs = &v
	}
	if !l.StreamTimeoutMs.IsNull() && !l.StreamTimeoutMs.IsUnknown() {
		v := int32(l.StreamTimeoutMs.ValueInt64())
		out.StreamTimeoutMs = &v
	}
	if !l.EnforceModelLimits.IsNull() && !l.EnforceModelLimits.IsUnknown() {
		v := l.EnforceModelLimits.ValueBool()
		out.EnforceModelLimits = &v
	}
	return out
}

// llmPolicyToModel maps the SDK response into Terraform state.
func llmPolicyToModel(ctx context.Context, tenantID string, pol *adminapi.LLMPolicy) LLMPolicyResourceModel {
	m := LLMPolicyResourceModel{
		TenantID: types.StringValue(tenantID),
	}
	if pol.Id != nil {
		m.PolicyID = types.StringValue(pol.Id.String())
	} else {
		m.PolicyID = types.StringNull()
	}
	m.ID = types.StringValue(tenantID + "/" + m.PolicyID.ValueString())

	m.Name = strPtrToTF(pol.Name)
	m.Description = strPtrToTF(pol.Description)
	m.Priority = int32PtrToTF(pol.Priority)
	m.Enabled = boolPtrOrDefault(pol.Enabled)
	m.SystemPrompt = strPtrToTF(pol.SystemPrompt)
	m.DeveloperPrompt = strPtrToTF(pol.DeveloperPrompt)
	m.Message = strPtrToTF(pol.Message)
	m.DisallowClientDeveloper = boolPtrOrDefault(pol.DisallowClientDeveloper)
	m.DisallowClientSystem = boolPtrOrDefault(pol.DisallowClientSystem)
	m.PromptCacheEnabled = boolPtrOrDefault(pol.PromptCacheEnabled)
	m.AllowProviderStorage = boolPtrOrDefault(pol.AllowProviderStorage)
	m.SummaryEnabled = boolPtrOrDefault(pol.SummaryEnabled)
	m.UseGatewayPrompts = boolPtrOrDefault(pol.UseGatewayPrompts)

	m.Version = int64PtrToTF(pol.Version)
	m.CreatedAt = timePtrToTF(pol.CreatedAt)
	m.CreatedBy = strPtrToTF(pol.CreatedBy)
	m.UpdatedAt = timePtrToTF(pol.UpdatedAt)
	m.UpdatedBy = strPtrToTF(pol.UpdatedBy)
	m.ManagedBy = enumPtrToTF(pol.ManagedBy)
	m.ManagedByClientID = strPtrToTF(pol.ManagedByClientId)
	m.ManagedByModule = strPtrToTF(pol.ManagedByModule)
	m.LastModifiedBy = enumPtrToTF(pol.LastModifiedBy)

	// provider_instances
	if pol.ProviderInstances != nil {
		elems := make([]attr.Value, 0, len(*pol.ProviderInstances))
		for _, s := range *pol.ProviderInstances {
			elems = append(elems, types.StringValue(s))
		}
		lv, _ := types.ListValue(types.StringType, elems)
		m.ProviderInstances = lv
	} else {
		m.ProviderInstances = types.ListNull(types.StringType)
	}

	// limits
	if pol.Limits != nil {
		m.Limits = limitsFromSDK(pol.Limits)
	}

	// criteria
	m.Criteria = criteriaListFromSDK(pol.Criteria)

	return m
}

func limitsFromSDK(l *gen.ModelSurfaceLimits) *LLMPolicyLimitsModel {
	if l == nil {
		return nil
	}
	out := &LLMPolicyLimitsModel{}
	out.MaxTokens = int32PtrToTF(l.MaxTokens)
	out.MaxRequestKb = int32PtrToTF(l.MaxRequestKb)
	out.MaxFilesPerRequest = int32PtrToTF(l.MaxFilesPerRequest)
	out.MaxImagesPerRequest = int32PtrToTF(l.MaxImagesPerRequest)
	out.MaxImageBytes = int64PtrToTF(l.MaxImageBytes)
	out.MaxAudioBytes = int64PtrToTF(l.MaxAudioBytes)
	out.MaxAudioDurationSec = int32PtrToTF(l.MaxAudioDurationSec)
	out.MaxToolArgumentsBytes = int32PtrToTF(l.MaxToolArgumentsBytes)
	out.RequestTimeoutMs = int32PtrToTF(l.RequestTimeoutMs)
	out.StreamTimeoutMs = int32PtrToTF(l.StreamTimeoutMs)
	out.EnforceModelLimits = boolPtrOrDefault(l.EnforceModelLimits)
	return out
}
