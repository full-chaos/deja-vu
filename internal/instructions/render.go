package instructions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Render either includes every required block or returns an error. Only optional
// defaults can be omitted. Budget counts UTF-8 context bytes, not model tokens.
func Render(result Result, budget int) (string, error) {
	if budget <= 0 || budget > DefaultBudget {
		return "", fmt.Errorf("budget must be between 1 and %d bytes", DefaultBudget)
	}
	if len(result.Conflicts) > 0 {
		return "", fmt.Errorf("unresolved instruction conflicts: %v", result.Conflicts)
	}
	if len(result.Applicable)+len(result.Conditional) == 0 {
		return "", nil
	}
	var required, optional []string
	for _, group := range []struct {
		items       []Selection
		conditional bool
	}{{result.Applicable, false}, {result.Conditional, true}} {
		for _, item := range group.items {
			r := item.Rule
			label := "APPLICABLE"
			if group.conditional {
				label = "CONDITIONAL"
			}
			absolute := ""
			if r.Absolute {
				absolute = "; absolute: explicit owner exception required"
			}
			text := fmt.Sprintf("\n[%s %s@%d; %s; %s; %s%s]\n%s\n", label, r.ID, r.Revision, r.Strength, r.Authority, r.Lifetime, absolute, r.Text)
			if group.conditional {
				scope, _ := json.Marshal(r.Scope)
				text += "Applies only when scope matches: " + string(scope) + " (" + item.Reason + ").\n"
			}
			if r.Runbook != nil {
				text += fmt.Sprintf("Runbook: %s sha256:%s\n", r.Runbook.Path, r.Runbook.SHA256)
				if group.conditional {
					text += "Runbook content deferred until applicability is known.\n"
				} else {
					b, err := readBounded(r.Runbook.Path, MaxRegistryBytes)
					if err != nil {
						return "", fmt.Errorf("runbook %s unavailable: %w", r.ID, err)
					}
					h := sha256.Sum256(b)
					if hex.EncodeToString(h[:]) != r.Runbook.SHA256 {
						return "", fmt.Errorf("runbook %s changed since approval; review and pin a new revision", r.ID)
					}
					if !utf8.Valid(b) {
						return "", fmt.Errorf("runbook %s is not UTF-8", r.ID)
					}
					text += "--- approved runbook ---\n" + string(b) + "\n--- end runbook ---\n"
				}
			}
			if r.Strength == "should" {
				optional = append(optional, text)
			} else {
				required = append(required, text)
			}
		}
	}
	body := fmt.Sprintf("Deja approved instructions, registry revision %d.\nRequired rules accumulate; ordering cannot waive one.\nConditional rules remain unresolved. Runbook delivery is not execution evidence.\n", result.Revision) + strings.Join(required, "")
	reserve := ""
	if len(optional) > 0 {
		reserve = fmt.Sprintf("\nOptional preferences omitted: %d.\n", len(optional))
	}
	if len(body)+len(reserve) > budget {
		return "", fmt.Errorf("required instruction packet exceeds %d-byte budget; narrow scope or shorten approved instructions", budget)
	}
	omitted := 0
	for _, text := range optional {
		if len(body)+len(text)+len(reserve) <= budget {
			body += text
		} else {
			omitted++
		}
	}
	if omitted > 0 {
		body += fmt.Sprintf("\nOptional preferences omitted: %d.\n", omitted)
	}
	return body, nil
}
