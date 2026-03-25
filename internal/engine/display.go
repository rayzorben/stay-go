package engine

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/rayben/stay-go/internal/state"
)

// ANSI escape codes — unexported; callers use the public Display* functions.
const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiGreen  = "\033[32m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
)

// ─── Action labels ────────────────────────────────────────────────────────────

// actionLabel returns the short, fixed-width action code shown in the table.
func actionLabel(a ActionType) string {
	switch a {
	case ActionAdd:
		return "+ ADD"
	case ActionRemove:
		return "- REM"
	case ActionUpdate:
		return "~ UPD"
	case ActionAdopt:
		return "* NEW"
	case ActionLevel:
		return "> LVL"
	case ActionTrack:
		return "~ TRK"
	case ActionSkip:
		return "! SKP"
	default:
		return "? ???"
	}
}

// actionColor wraps text in the ANSI colour appropriate for the action.
func actionColor(a ActionType) func(string) string {
	switch a {
	case ActionAdd:
		return func(s string) string { return ansiGreen + s + ansiReset }
	case ActionRemove:
		return func(s string) string { return ansiRed + s + ansiReset }
	case ActionUpdate:
		return func(s string) string { return ansiYellow + s + ansiReset }
	case ActionAdopt:
		return func(s string) string { return ansiCyan + s + ansiReset }
	case ActionLevel:
		return func(s string) string { return ansiCyan + ansiDim + s + ansiReset }
	case ActionTrack:
		return func(s string) string { return ansiDim + s + ansiReset }
	case ActionSkip:
		return func(s string) string { return ansiYellow + s + ansiReset }
	default:
		return func(s string) string { return s }
	}
}

// resourceLabel returns the singular display name for a resource type.
var resourceLabel = map[string]string{
	"packages": "package",
	"groups":   "group",
	"users":    "user",
	"services": "service",
	"scripts":  "script",
	"commands": "command",
	"secrets":  "secret",
}

func typeLabel(resourceType string) string {
	if l, ok := resourceLabel[resourceType]; ok {
		return l
	}
	return resourceType
}

// ─── Table rendering ──────────────────────────────────────────────────────────

// tableRow holds the plain (un-coloured) string values for one plan row.
type tableRow struct {
	action       string // e.g. "+ ADD"
	resourceType string // e.g. "package"
	level        string // e.g. "common" or "user:rayben"
	item         string // DisplayName only (for column width computation)
	description  string // optional contextual detail shown in dim after item
	node         *PlanNode
}

// actionSortOrder returns a numeric priority for display sorting.
// Lower numbers appear first; SKIP is always last.
func actionSortOrder(a ActionType) int {
	switch a {
	case ActionAdd:
		return 0
	case ActionRemove:
		return 1
	case ActionUpdate:
		return 2
	case ActionAdopt:
		return 3
	case ActionLevel:
		return 4
	case ActionSkip:
		return 5
	default:
		return 6
	}
}

// buildRows converts a plan into displayable rows sorted by action priority,
// then level, then resource type. TRACK nodes are always omitted.
// Skip reasons are stored on the node and rendered as a continuation line;
// item column widths are computed only from the bare DisplayName.
func buildRows(nodes []*PlanNode) []tableRow {
	rows := make([]tableRow, 0, len(nodes))
	for _, n := range nodes {
		if n.Action == ActionTrack {
			continue
		}
		level := n.Level
		if level == "" {
			level = "common"
		}
		desc := ""
		if n.Action != ActionSkip {
			desc = n.Description
		}
		rows = append(rows, tableRow{
			action:       actionLabel(n.Action),
			resourceType: typeLabel(n.ResourceType),
			level:        level,
			item:         n.DisplayName,
			description:  desc,
			node:         n,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		oa, ob := actionSortOrder(rows[i].node.Action), actionSortOrder(rows[j].node.Action)
		if oa != ob {
			return oa < ob
		}
		if rows[i].level != rows[j].level {
			return rows[i].level < rows[j].level
		}
		return rows[i].resourceType < rows[j].resourceType
	})
	return rows
}

// DisplayPlan writes the formatted tabular execution plan to w.
// Columns: ACTION | LEVEL | RESOURCE | ITEM
func DisplayPlan(w io.Writer, nodes []*PlanNode) {
	if len(nodes) == 0 {
		return
	}
	rows := buildRows(nodes)

	// Compute column widths from data (minimum = header width).
	w1 := len("ACTION")
	w2 := len("LEVEL")
	w3 := len("RESOURCE")
	w4 := len("ITEM")
	for _, r := range rows {
		if l := len(r.action); l > w1 {
			w1 = l
		}
		if l := len(r.level); l > w2 {
			w2 = l
		}
		if l := len(r.resourceType); l > w3 {
			w3 = l
		}
		if l := len(r.item); l > w4 {
			w4 = l
		}
	}

	// Header.
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s%s%s  %s%s%s  %s%s%s  %s%s%s\n",
		ansiBold, padCenter("ACTION", w1), ansiReset,
		ansiBold, padCenter("LEVEL", w2), ansiReset,
		ansiBold, padCenter("RESOURCE", w3), ansiReset,
		ansiBold, padCenter("ITEM", w4), ansiReset,
	)

	// Separator.
	sep := fmt.Sprintf("  %s  %s  %s  %s",
		strings.Repeat("-", w1),
		strings.Repeat("-", w2),
		strings.Repeat("-", w3),
		strings.Repeat("-", w4),
	)
	fmt.Fprintln(w, ansiDim+sep+ansiReset)

	// Data rows.
	for _, r := range rows {
		color := actionColor(r.node.Action)
		suffix := ""
		if r.description != "" {
			suffix = "  " + ansiDim + r.description + ansiReset
		}
		fmt.Fprintf(w, "  %s  %-*s  %-*s  %-*s%s\n",
			color(padLeft(r.action, w1)),
			w2, r.level,
			w3, r.resourceType,
			w4, r.item,
			suffix,
		)
		// Skip reasons are rendered as a dim continuation line beneath the row.
		if r.node.Action == ActionSkip && r.node.SkipReason != "" {
			for _, part := range strings.Split(r.node.SkipReason, "; ") {
				fmt.Fprintf(w, "  %s↳ %s%s\n", ansiDim, part, ansiReset)
			}
		}
	}
	fmt.Fprintln(w)
}

// DisplaySummary writes the compact change summary line.
func DisplaySummary(w io.Writer, nodes []*PlanNode) {
	counts := make(map[ActionType]int)
	for _, n := range nodes {
		counts[n.Action]++
	}
	fmt.Fprintf(w, "Summary:  %s+%d add%s  %s-%d remove%s  ~%d update  %s*%d new%s  %s>%d moved%s  %s!%d skipped%s  %s%d managed%s\n\n",
		ansiGreen, counts[ActionAdd], ansiReset,
		ansiRed, counts[ActionRemove], ansiReset,
		counts[ActionUpdate],
		ansiCyan, counts[ActionAdopt], ansiReset,
		ansiCyan, counts[ActionLevel], ansiReset,
		ansiYellow, counts[ActionSkip], ansiReset,
		ansiDim, counts[ActionTrack], ansiReset,
	)
}

// DisplayExecutionProgress prints the pending action line immediately before
// execution begins. It ends with \r (no newline) so that DisplayExecutionResult
// can overwrite it in place once the outcome is known.
func DisplayExecutionProgress(w io.Writer, node *PlanNode) {
	level := node.Level
	if level == "" {
		level = "common"
	}
	color := actionColor(node.Action)
	fmt.Fprintf(w, "  %s  %-14s  %-8s  %s\r",
		color(padLeft(actionLabel(node.Action), 5)),
		level,
		typeLabel(node.ResourceType),
		node.DisplayName,
	)
}

// DisplayExecutionResult overwrites the pending progress line with the final
// outcome. \r\033[K repositions and clears the line before printing.
func DisplayExecutionResult(w io.Writer, node *PlanNode, err error) {
	level := node.Level
	if level == "" {
		level = "common"
	}

	action := actionLabel(node.Action)
	if err != nil {
		action = "✗ ERR"
	}
	color := actionColor(node.Action)
	if err != nil {
		color = func(s string) string { return ansiRed + s + ansiReset }
	}

	result := ""
	if err != nil {
		result = fmt.Sprintf("  %s%v%s", ansiRed, err, ansiReset)
	}

	fmt.Fprintf(w, "\r\033[K  %s  %-14s  %-8s  %s%s\n",
		color(padLeft(action, 5)),
		level,
		typeLabel(node.ResourceType),
		node.DisplayName,
		result,
	)
}

// Confirm prints the prompt and reads a Y/n response from r.
// Returns true if the user presses Enter or types y/Y.
func Confirm(w io.Writer, r io.Reader) (bool, error) {
	fmt.Fprintf(w, "Proceed with execution? %s[Y/n]%s: ", ansiBold, ansiReset)
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, err
		}
		return false, nil // EOF
	}
	resp := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return resp == "" || resp == "y" || resp == "yes", nil
}

// ─── String padding helpers ───────────────────────────────────────────────────

func padLeft(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func padCenter(s string, width int) string {
	if len(s) >= width {
		return s
	}
	total := width - len(s)
	left := total / 2
	right := total - left
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", right)
}

// ─── Show command ─────────────────────────────────────────────────────────────

// canonicalOrder defines the display order for resource types in --show output.
var canonicalOrder = []string{"packages", "groups", "users", "services", "scripts", "commands", "secrets"}

// DisplayShow writes a read-only table of all currently-tracked nodes to w.
// scope controls what is printed:
//
//	"all"       — variables section + all resource types
//	"variables" — only the resolved variables map
//	any other   — only nodes whose resource type matches the scope
func DisplayShow(w io.Writer, st *state.State, vars map[string]string, scope string) {
	// ── Variables section ────────────────────────────────────────────────────
	if scope == "all" || scope == "variables" {
		keys := make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(w, "%sVariables%s\n", ansiBold, ansiReset)
		w2 := 0
		for _, k := range keys {
			if l := len(k) + 3; l > w2 { // "${" + key + "}"
				w2 = l
			}
		}
		for _, k := range keys {
			token := "${" + k + "}"
			fmt.Fprintf(w, "  %-*s  %s\n", w2, token, vars[k])
		}
		fmt.Fprintln(w)
		if scope == "variables" {
			return
		}
	}

	// ── Tracked nodes table ──────────────────────────────────────────────────
	type showRow struct {
		resourceType string
		level        string
		item         string
		hash         string
		tracked      string
	}

	// Collect rows, ordered by canonical resource type then item name.
	orderIdx := make(map[string]int, len(canonicalOrder))
	for i, t := range canonicalOrder {
		orderIdx[t] = i
	}

	var rows []showRow
	for id, entry := range st.Nodes {
		slash := strings.Index(id, "/")
		if slash < 0 {
			continue
		}
		resType := id[:slash]
		item := id[slash+1:]

		if scope != "all" && resType != scope {
			continue
		}

		level := entry.Level
		if level == "" {
			level = "common"
		}
		rows = append(rows, showRow{
			resourceType: typeLabel(resType),
			level:        level,
			item:         item,
			hash:         entry.Hash,
			tracked:      entry.TrackedAt.Format("2006-01-02"),
		})
	}

	if len(rows) == 0 {
		fmt.Fprintln(w, ansiDim+"(nothing tracked)"+ansiReset)
		return
	}

	sort.Slice(rows, func(i, j int) bool {
		oi := orderIdx[rows[i].resourceType]
		oj := orderIdx[rows[j].resourceType]
		if oi != oj {
			return oi < oj
		}
		return rows[i].item < rows[j].item
	})

	// Compute column widths.
	w1, w2, w3, w4, w5 := len("RESOURCE"), len("LEVEL"), len("ITEM"), len("HASH"), len("TRACKED")
	for _, r := range rows {
		if l := len(r.resourceType); l > w1 {
			w1 = l
		}
		if l := len(r.level); l > w2 {
			w2 = l
		}
		if l := len(r.item); l > w3 {
			w3 = l
		}
		if l := len(r.hash); l > w4 {
			w4 = l
		}
	}

	// Header.
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s%s%s  %s%s%s  %s%s%s  %s%s%s  %s%s%s\n",
		ansiBold, padCenter("RESOURCE", w1), ansiReset,
		ansiBold, padCenter("LEVEL", w2), ansiReset,
		ansiBold, padCenter("ITEM", w3), ansiReset,
		ansiBold, padCenter("HASH", w4), ansiReset,
		ansiBold, padCenter("TRACKED", w5), ansiReset,
	)
	sep := fmt.Sprintf("  %s  %s  %s  %s  %s",
		strings.Repeat("-", w1), strings.Repeat("-", w2),
		strings.Repeat("-", w3), strings.Repeat("-", w4),
		strings.Repeat("-", w5),
	)
	fmt.Fprintln(w, ansiDim+sep+ansiReset)

	for _, r := range rows {
		fmt.Fprintf(w, "  %-*s  %-*s  %-*s  %s%s%s  %s\n",
			w1, r.resourceType,
			w2, r.level,
			w3, r.item,
			ansiDim, r.hash, ansiReset,
			r.tracked,
		)
	}
	fmt.Fprintln(w)
}
