// Package thinking provides unified thinking configuration processing.
package thinking

import "strings"

// NormalizeCodexModelName strips Codex reasoning suffixes from the base model name.
//
// It first removes any generic thinking suffix in the form model(value), then
// strips a trailing Codex reasoning level suffix from the base model name when
// present: -low, -medium, -high, or -xhigh.
func NormalizeCodexModelName(model string) string {
	baseModel := strings.TrimSpace(ParseSuffix(model).ModelName)
	if baseModel == "" {
		return ""
	}
	if idx := strings.LastIndex(baseModel, "-"); idx > 0 {
		suffix := strings.ToLower(strings.TrimSpace(baseModel[idx+1:]))
		if suffix == "low" || suffix == "medium" || suffix == "high" || suffix == "xhigh" {
			return strings.TrimSpace(baseModel[:idx])
		}
	}
	return baseModel
}
