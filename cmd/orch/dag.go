package main

import (
	"fmt"
	"strings"

	"github.com/gordonwei/orch/pkg/planner"
)

// renderDAG builds a text-based DAG visualization using topological levels.
// Steps at the same level (no dependencies between them) are shown on the same row.
func renderDAG(steps []planner.Step) string {
	if len(steps) == 0 {
		return "(empty plan)\n"
	}

	// Build index: stepID → position in steps slice
	stepIndex := make(map[string]int)
	for i, s := range steps {
		stepIndex[s.ID] = i
	}

	// Compute topological levels (longest path from root)
	levels := make([]int, len(steps))
	for i, s := range steps {
		maxDepLevel := -1
		for _, dep := range s.DependsOn {
			if idx, ok := stepIndex[dep]; ok {
				if levels[idx] > maxDepLevel {
					maxDepLevel = levels[idx]
				}
			}
		}
		levels[i] = maxDepLevel + 1
	}

	// Group steps by level
	maxLevel := 0
	for _, l := range levels {
		if l > maxLevel {
			maxLevel = l
		}
	}

	levelGroups := make([][]int, maxLevel+1)
	for i, l := range levels {
		levelGroups[l] = append(levelGroups[l], i)
	}

	var sb strings.Builder

	// Simple case: linear chain (each level has exactly 1 step)
	allSingle := true
	for _, group := range levelGroups {
		if len(group) != 1 {
			allSingle = false
			break
		}
	}
	if allSingle {
		for i, s := range steps {
			if i < len(steps)-1 {
				sb.WriteString(fmt.Sprintf("[%s] ──▶ ", s.ID))
			} else {
				sb.WriteString(fmt.Sprintf("[%s]", s.ID))
			}
		}
		sb.WriteString("\n")
		return sb.String()
	}

	// General case: show fan-in / fan-out patterns
	for lvl := 0; lvl <= maxLevel; lvl++ {
		group := levelGroups[lvl]

		if lvl < maxLevel {
			nextGroup := levelGroups[lvl+1]

			if len(group) == 1 && len(nextGroup) == 1 {
				// One-to-one
				sb.WriteString(fmt.Sprintf("[%s] ──▶ ", steps[group[0]].ID))
			} else if len(group) > 1 && len(nextGroup) >= 1 {
				// Fan-in: multiple steps converge to next level
				for i, idx := range group {
					if i == 0 {
						sb.WriteString(fmt.Sprintf("[%s] ──┐\n", steps[idx].ID))
					} else if i == len(group)-1 {
						sb.WriteString(fmt.Sprintf("[%s] ──┘\n", steps[idx].ID))
					} else {
						sb.WriteString(fmt.Sprintf("[%s] ──┤\n", steps[idx].ID))
					}
				}
				// Draw the merge point → next level targets
				padding := strings.Repeat(" ", 12)
				for _, nIdx := range nextGroup {
					sb.WriteString(fmt.Sprintf("%s├──▶ [%s]\n", padding, steps[nIdx].ID))
				}
				lvl++ // skip the next level since we already drew it
			} else if len(group) == 1 && len(nextGroup) > 1 {
				// Fan-out: one step fans out to multiple
				sb.WriteString(fmt.Sprintf("[%s] ──┬──▶ [%s]\n", steps[group[0]].ID, steps[nextGroup[0]].ID))
				padding := strings.Repeat(" ", len(steps[group[0]].ID)+3)
				for i := 1; i < len(nextGroup); i++ {
					connector := "├"
					if i == len(nextGroup)-1 {
						connector = "└"
					}
					sb.WriteString(fmt.Sprintf("%s%s──▶ [%s]\n", padding, connector, steps[nextGroup[i]].ID))
				}
				lvl++ // skip next level
			} else {
				// Fallback: just list
				for _, idx := range group {
					sb.WriteString(fmt.Sprintf("[%s]\n", steps[idx].ID))
				}
				sb.WriteString("  │\n  ▼\n")
			}
		} else {
			// Last level
			for _, idx := range group {
				sb.WriteString(fmt.Sprintf("[%s]\n", steps[idx].ID))
			}
		}
	}

	return sb.String()
}
