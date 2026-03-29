package agent

import (
	"sort"
	"strings"
)

func resolveAgentToolAllowlist(definition AgentContextDefinition) []string {
	if definition.Agent == nil || definition.Agent.Frontmatter.Tools == nil {
		return nil
	}

	allowlist := make(map[string]struct{}, len(definition.Agent.Frontmatter.Tools))
	for _, raw := range definition.Agent.Frontmatter.Tools {
		trimmed := strings.ToLower(strings.TrimSpace(raw))
		if trimmed == "" {
			continue
		}
		allowlist[trimmed] = struct{}{}
	}

	result := make([]string, 0, len(allowlist))
	for name := range allowlist {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
