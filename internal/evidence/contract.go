package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ReadContract(path string) (Contract, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Contract{}, fmt.Errorf("read contract: %w", err)
	}

	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return Contract{}, errors.New("contract is empty")
	}

	if strings.HasPrefix(trimmed, "{") {
		var contract Contract
		if err := json.Unmarshal(data, &contract); err != nil {
			return Contract{}, fmt.Errorf("parse JSON contract: %w", err)
		}
		return contract, nil
	}

	return parseContractYAML(trimmed)
}

func parseContractYAML(input string) (Contract, error) {
	var contract Contract
	var section string
	var currentRule *EnvRule

	lines := strings.Split(input, "\n")
	for lineNumber, raw := range lines {
		line := strings.TrimRight(raw, " \t\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		indent := leadingSpaces(line)
		text := strings.TrimSpace(line)

		if indent == 0 {
			key, value, ok := splitYAMLKeyValue(text)
			if !ok {
				return Contract{}, fmt.Errorf("line %d: expected key/value", lineNumber+1)
			}
			section = key
			currentRule = nil
			switch key {
			case "service":
				contract.Service = parseYAMLScalar(value)
			case "owners":
				if strings.TrimSpace(value) != "" {
					contract.Owners = parseYAMLList(value)
				}
			case "build":
				if strings.TrimSpace(value) != "" {
					return Contract{}, fmt.Errorf("line %d: build must be a mapping", lineNumber+1)
				}
			case "env_rules":
				if strings.TrimSpace(value) == "[]" {
					contract.EnvRules = nil
				} else if strings.TrimSpace(value) != "" {
					return Contract{}, fmt.Errorf("line %d: env_rules must be a list of mappings", lineNumber+1)
				}
			case "risk_level":
				contract.RiskLevel = parseYAMLScalar(value)
			case "risk_paths":
				if v := strings.TrimSpace(value); v == "[]" {
					contract.RiskPaths = nil
				} else if v != "" {
					contract.RiskPaths = parseYAMLList(value)
				}
			default:
				return Contract{}, fmt.Errorf("line %d: unsupported contract field %q", lineNumber+1, key)
			}
			continue
		}

		switch section {
		case "owners":
			if indent != 2 || !strings.HasPrefix(text, "- ") {
				return Contract{}, fmt.Errorf("line %d: owners entries must use '- value'", lineNumber+1)
			}
			contract.Owners = append(contract.Owners, parseYAMLScalar(strings.TrimSpace(strings.TrimPrefix(text, "- "))))
		case "risk_paths":
			if indent != 2 || !strings.HasPrefix(text, "- ") {
				return Contract{}, fmt.Errorf("line %d: risk_paths entries must use '- value'", lineNumber+1)
			}
			contract.RiskPaths = append(contract.RiskPaths, parseYAMLScalar(strings.TrimSpace(strings.TrimPrefix(text, "- "))))
		case "build":
			if indent != 2 {
				return Contract{}, fmt.Errorf("line %d: build entries must be indented two spaces", lineNumber+1)
			}
			key, value, ok := splitYAMLKeyValue(text)
			if !ok {
				return Contract{}, fmt.Errorf("line %d: expected build key/value", lineNumber+1)
			}
			switch key {
			case "verify":
				contract.Build.Verify = parseYAMLScalar(value)
			case "verify_thick":
				contract.Build.VerifyThick = parseYAMLScalar(value)
			default:
				return Contract{}, fmt.Errorf("line %d: unsupported build field %q", lineNumber+1, key)
			}
		case "env_rules":
			if indent == 2 && strings.HasPrefix(text, "- ") {
				rule := EnvRule{}
				contract.EnvRules = append(contract.EnvRules, rule)
				currentRule = &contract.EnvRules[len(contract.EnvRules)-1]
				rest := strings.TrimSpace(strings.TrimPrefix(text, "- "))
				if rest != "" {
					key, value, ok := splitYAMLKeyValue(rest)
					if !ok {
						return Contract{}, fmt.Errorf("line %d: expected env_rules key/value", lineNumber+1)
					}
					if err := assignEnvRule(currentRule, key, value); err != nil {
						return Contract{}, fmt.Errorf("line %d: %w", lineNumber+1, err)
					}
				}
				continue
			}
			if indent != 4 || currentRule == nil {
				return Contract{}, fmt.Errorf("line %d: env_rules entries must be list mappings", lineNumber+1)
			}
			key, value, ok := splitYAMLKeyValue(text)
			if !ok {
				return Contract{}, fmt.Errorf("line %d: expected env_rules key/value", lineNumber+1)
			}
			if err := assignEnvRule(currentRule, key, value); err != nil {
				return Contract{}, fmt.Errorf("line %d: %w", lineNumber+1, err)
			}
		default:
			return Contract{}, fmt.Errorf("line %d: unexpected indented content", lineNumber+1)
		}
	}

	return contract, nil
}

func assignEnvRule(rule *EnvRule, key string, value string) error {
	switch key {
	case "id":
		rule.ID = parseYAMLScalar(value)
	case "applies_to":
		rule.AppliesTo = parseYAMLScalar(value)
	case "reminder":
		rule.Reminder = parseYAMLScalar(value)
	case "check":
		rule.Check = parseYAMLScalar(value)
	case "assert":
		rule.Assert = parseYAMLScalar(value)
	case "severity":
		rule.Severity = parseYAMLScalar(value)
	case "gate_action":
		rule.GateAction = parseYAMLScalar(value)
	case "command_prefixes":
		rule.CommandPrefixes = parseYAMLList(value)
	default:
		return fmt.Errorf("unsupported env_rules field %q", key)
	}
	return nil
}

func validateContract(contract Contract, risk string) error {
	if !validRisk(risk) {
		return GateError{Check: "risk", Reason: fmt.Sprintf("invalid risk %q; expected high, med, or low", risk), ExitCode: exitBlock}
	}
	if strings.TrimSpace(contract.Service) == "" {
		return GateError{Check: "contract", Reason: "missing required field service", ExitCode: exitBlock}
	}
	if len(contract.Owners) == 0 {
		return GateError{Check: "contract", Reason: "missing required field owners", ExitCode: exitBlock}
	}
	for _, owner := range contract.Owners {
		if strings.TrimSpace(owner) == "" {
			return GateError{Check: "contract", Reason: "owners must not contain empty values", ExitCode: exitBlock}
		}
	}
	if strings.TrimSpace(contract.RiskLevel) == "" {
		return GateError{Check: "contract", Reason: "missing required field risk_level", ExitCode: exitBlock}
	}
	if !validRisk(contract.RiskLevel) {
		return GateError{Check: "contract", Reason: fmt.Sprintf("invalid contract risk_level %q", contract.RiskLevel), ExitCode: exitBlock}
	}
	if riskRank(risk) < riskRank(contract.RiskLevel) {
		return GateError{Check: "risk", Reason: fmt.Sprintf("requested risk %q is lower than contract risk_level %q", risk, contract.RiskLevel), ExitCode: exitBlock}
	}
	if strings.TrimSpace(contract.Build.Verify) == "" {
		return GateError{Check: "build.verify", Reason: "missing required command build.verify", ExitCode: exitBlock}
	}
	if risk == "high" && strings.TrimSpace(contract.Build.VerifyThick) == "" {
		return GateError{Check: "build.verify_thick", Reason: "missing required command build.verify_thick for high risk", ExitCode: exitBlock}
	}
	if risk == "med" || risk == "high" {
		if len(contract.EnvRules) == 0 {
			return GateError{
				Check:      "env_rules",
				Reason:     fmt.Sprintf("missing required env_rules for %s risk", risk),
				Suggestion: missingEnvRuleTemplate(contract, risk),
				ExitCode:   exitBlock,
			}
		}
		for index, rule := range contract.EnvRules {
			reminder := strings.TrimSpace(rule.Reminder)
			if reminder == "" {
				reminder = strings.TrimSpace(rule.Check)
			}
			if strings.TrimSpace(rule.ID) == "" || strings.TrimSpace(rule.AppliesTo) == "" || reminder == "" || strings.TrimSpace(rule.Severity) == "" {
				return GateError{Check: "env_rules", Reason: fmt.Sprintf("env_rules[%d] is missing id, applies_to, reminder/check, or severity", index), ExitCode: exitBlock}
			}
			if rule.Severity != "blocking" && rule.Severity != "warning" {
				return GateError{Check: "env_rules", Reason: fmt.Sprintf("env_rules[%d] has invalid severity %q", index, rule.Severity), ExitCode: exitBlock}
			}
			if rule.GateAction != "" && rule.GateAction != "allow" && rule.GateAction != "prompt" && rule.GateAction != "forbidden" {
				return GateError{Check: "env_rules", Reason: fmt.Sprintf("env_rules[%d] has invalid gate_action %q", index, rule.GateAction), ExitCode: exitBlock}
			}
			if len(rule.CommandPrefixes) > 0 && rule.GateAction == "" {
				return GateError{Check: "env_rules", Reason: fmt.Sprintf("env_rules[%d] has command_prefixes without gate_action", index), ExitCode: exitBlock}
			}
		}
	}
	return nil
}

func missingEnvRuleTemplate(contract Contract, risk string) string {
	service := slugValue(contract.Service, "service")
	return fmt.Sprintf(`Add an env_rules entry before rerunning %s-risk evidence:
env_rules:
  - id: %s-runner-no-net
    applies_to: %s_evidence_runner
    reminder: "No curl/wget/pip/npm or other network dependency fetches during %s evidence runs; dependencies must be vendored or preinstalled."
    assert: "test -z \"${EZH_NETWORK_FETCH_DETECTED:-}\""
    severity: blocking`, risk, service, service, service)
}

func slugValue(value string, fallback string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return fallback
	}
	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			builder.WriteRune(char)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return fallback
	}
	return slug
}

func validRisk(value string) bool {
	return value == "high" || value == "med" || value == "low"
}

func riskRank(value string) int {
	switch value {
	case "low":
		return 1
	case "med":
		return 2
	case "high":
		return 3
	default:
		return 0
	}
}

func contractProjectRoot(contractPath string) string {
	dir := filepath.Dir(contractPath)
	if filepath.Base(dir) == ".harness" {
		return filepath.Dir(dir)
	}
	return dir
}

func splitYAMLKeyValue(text string) (string, string, bool) {
	key, value, ok := strings.Cut(text, ":")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", false
	}
	return key, strings.TrimSpace(value), true
}

func parseYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		first := value[0]
		last := value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func parseYAMLList(value string) []string {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		if value == "" {
			return nil
		}
		return []string{parseYAMLScalar(value)}
	}
	body := strings.TrimSpace(value[1 : len(value)-1])
	if body == "" {
		return nil
	}
	parts := strings.Split(body, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		items = append(items, parseYAMLScalar(strings.TrimSpace(part)))
	}
	return items
}

func leadingSpaces(value string) int {
	count := 0
	for _, char := range value {
		if char != ' ' {
			break
		}
		count++
	}
	return count
}
