package engine

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/rayben/stay-go/internal/state"
)

// ─── ANSI escape codes ────────────────────────────────────────────────────────

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiGreen  = "\033[32m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
)

// ─── Display metrics ──────────────────────────────────────────────────────────

// displayMetrics holds column widths computed by DisplayPlan and reused by the
// execution display functions for visual consistency. Defaults handle the
// QuietPlan case where DisplayPlan is never called.
var displayMetrics = struct{ nameW, typeW int }{nameW: 40, typeW: 9}

// ─── Colour and symbol helpers ────────────────────────────────────────────────

// actionColor wraps text in the ANSI colour for the given action.
func actionColor(a ActionType) func(string) string {
	switch a {
	case ActionAdd, ActionAdopt:
		return func(s string) string { return ansiGreen + s + ansiReset }
	case ActionRemove:
		return func(s string) string { return ansiRed + s + ansiReset }
	case ActionUpdate:
		return func(s string) string { return ansiYellow + s + ansiReset }
	case ActionLevel:
		return func(s string) string { return ansiCyan + s + ansiReset }
	case ActionSkip:
		return func(s string) string { return ansiDim + s + ansiReset }
	default:
		return func(s string) string { return s }
	}
}

// itemSymbol returns the single character shown next to each plan item.
func itemSymbol(a ActionType) string {
	switch a {
	case ActionAdd, ActionAdopt:
		return "+"
	case ActionUpdate:
		return "~"
	case ActionLevel:
		return "="
	case ActionRemove:
		return "-"
	case ActionSkip:
		return "!"
	default:
		return "·"
	}
}

// ─── Resource type labels ─────────────────────────────────────────────────────

var resourceLabel = map[string]string{
	"packages":   "package",
	"groups":     "group",
	"users":      "user",
	"services":   "service",
	"scripts":    "script",
	"files":      "file",
	"commands":   "command",
	"secrets":    "secret",
	"containers": "container",
	"distrobox":  "distrobox",
	"json":       "json",
}

func typeLabel(resourceType string) string {
	if l, ok := resourceLabel[resourceType]; ok {
		return l
	}
	return resourceType
}

// ─── Canonical resource order ─────────────────────────────────────────────────

// canonicalOrder defines the display order for resource types.
var canonicalOrder = []string{
	"packages", "groups", "users", "services", "scripts",
	"files", "commands", "secrets", "containers", "distrobox", "json",
}

var canonicalIndex = func() map[string]int {
	m := make(map[string]int, len(canonicalOrder))
	for i, t := range canonicalOrder {
		m[t] = i
	}
	return m
}()

// ─── Action groups ────────────────────────────────────────────────────────────

// groupID classifies actions into one of four display sections.
type groupID int

const (
	grpAdding   groupID = 0
	grpUpdating groupID = 1
	grpMoved    groupID = 2
	grpRemoving groupID = 3
	grpSkipped  groupID = 4
)

type groupDef struct {
	label string
	color func(string) string
}

var planGroups = [5]groupDef{
	grpAdding:   {"adding", actionColor(ActionAdd)},
	grpUpdating: {"updating", actionColor(ActionUpdate)},
	grpMoved:    {"moved", actionColor(ActionLevel)},
	grpRemoving: {"removing", actionColor(ActionRemove)},
	grpSkipped:  {"skipped", actionColor(ActionSkip)},
}

// nodeGroupID maps an ActionType to its display section.
// Returns (group, true) or (0, false) for hidden ActionTrack nodes.
func nodeGroupID(a ActionType) (groupID, bool) {
	switch a {
	case ActionAdd, ActionAdopt:
		return grpAdding, true
	case ActionUpdate:
		return grpUpdating, true
	case ActionLevel:
		return grpMoved, true
	case ActionRemove:
		return grpRemoving, true
	case ActionSkip:
		return grpSkipped, true
	default:
		return 0, false
	}
}

// ─── Low-level rendering primitives ──────────────────────────────────────────

// sectionHeader writes: `  ─── {label} {─────...}\n`
// The label is rendered in its section colour; surrounding dashes are dim.
func sectionHeader(w io.Writer, label string, color func(string) string, tw int) {
	// Visible text structure: "  ─── " (6) + label + " " + trailing dashes
	const preamble = "  ─── "
	trailing := tw - len(preamble) - len(label) - 1
	if trailing < 1 {
		trailing = 1
	}
	fmt.Fprintf(w, "%s%s%s%s %s%s%s\n",
		ansiDim, preamble, ansiReset,
		color(label),
		ansiDim, strings.Repeat("─", trailing), ansiReset,
	)
}

// divider writes a full-width dim horizontal rule.
func divider(w io.Writer, tw int) {
	if tw > 2 {
		fmt.Fprintf(w, "  %s%s%s\n", ansiDim, strings.Repeat("─", tw-2), ansiReset)
	}
}

// noteLine writes a dim `     └ {text}` continuation beneath a plan item.
// text is truncated to fit within tw.
func noteLine(w io.Writer, text string, tw int) {
	const prefix = "     └ " // 7 visible chars
	maxText := tw - len(prefix)
	if maxText < 1 {
		maxText = 1
	}
	if len(text) > maxText {
		text = text[:maxText-1] + "…"
	}
	fmt.Fprintf(w, "%s%s%s%s\n", ansiDim, prefix, text, ansiReset)
}

// writeSummaryLine writes the compact line shown above the plan groups:
// `  +N add  ~N update  -N remove  !N skip  ·  N managed`
func writeSummaryLine(w io.Writer, nodes []*PlanNode) {
	counts := make(map[ActionType]int, 8)
	for _, n := range nodes {
		counts[n.Action]++
	}
	add := counts[ActionAdd] + counts[ActionAdopt]
	upd := counts[ActionUpdate]
	mov := counts[ActionLevel]
	rem := counts[ActionRemove]
	skp := counts[ActionSkip]
	managed := counts[ActionTrack]

	var parts []string
	if add > 0 {
		parts = append(parts, fmt.Sprintf("%s+%d add%s", ansiGreen, add, ansiReset))
	}
	if upd > 0 {
		parts = append(parts, fmt.Sprintf("%s~%d update%s", ansiYellow, upd, ansiReset))
	}
	if mov > 0 {
		parts = append(parts, fmt.Sprintf("%s=%d moved%s", ansiCyan, mov, ansiReset))
	}
	if rem > 0 {
		parts = append(parts, fmt.Sprintf("%s-%d remove%s", ansiRed, rem, ansiReset))
	}
	if skp > 0 {
		parts = append(parts, fmt.Sprintf("%s!%d skip%s", ansiDim, skp, ansiReset))
	}
	parts = append(parts, fmt.Sprintf("%s·  %d managed%s", ansiDim, managed, ansiReset))
	fmt.Fprintf(w, "  %s\n", strings.Join(parts, "  "))
}

// formatDur formats a duration as a compact decimal-second string, e.g. "0.3s".
func formatDur(d time.Duration) string {
	s := d.Seconds()
	if s < 10 {
		return fmt.Sprintf("%.1fs", s)
	}
	return fmt.Sprintf("%.0fs", s)
}

// ─── Plan display ─────────────────────────────────────────────────────────────

// DisplayPlan writes the grouped plan to w and updates displayMetrics.
//
// Layout per item:
//
//	  {sym}  {name}{spaces}{type}  {level}
//
// where type+level is right-aligned to the terminal edge.
func DisplayPlan(w io.Writer, nodes []*PlanNode) {
	if len(nodes) == 0 {
		return
	}

	tw := termWidth()

	// Partition visible nodes into groups (TRACK is hidden).
	type vnode struct {
		n     *PlanNode
		grp   groupID
		typ   string // typeLabel result
		level string
	}
	var visible []vnode
	for _, n := range nodes {
		g, ok := nodeGroupID(n.Action)
		if !ok {
			continue
		}
		lv := n.Level
		if lv == "" {
			lv = "common"
		}
		visible = append(visible, vnode{n: n, grp: g, typ: typeLabel(n.ResourceType), level: lv})
	}
	if len(visible) == 0 {
		return
	}

	// Compute right-block column widths across all visible nodes.
	typeW, levelW := 0, 6 // "common" minimum
	for _, v := range visible {
		if l := len(v.typ); l > typeW {
			typeW = l
		}
		if l := len(v.level); l > levelW {
			levelW = l
		}
	}

	// rightBlockLen = typeW + 2 ("  ") + levelW; this block is right-aligned.
	// maxNameLen: what remains after prefix (5) + min-gap (2) + right block.
	const prefixLen = 5 // "  {sym}  "
	const minGap = 2
	rightBlockLen := typeW + 2 + levelW
	maxNameLen := tw - prefixLen - minGap - rightBlockLen
	if maxNameLen < 10 {
		maxNameLen = 10
	}

	// Save for execution display.
	displayMetrics.nameW = maxNameLen
	displayMetrics.typeW = typeW

	// Sort: by group (fixed order 0–3), then canonical resource type, then name.
	sort.SliceStable(visible, func(i, j int) bool {
		vi, vj := visible[i], visible[j]
		if vi.grp != vj.grp {
			return vi.grp < vj.grp
		}
		oi := canonicalIndex[vi.n.ResourceType]
		oj := canonicalIndex[vj.n.ResourceType]
		if oi != oj {
			return oi < oj
		}
		return vi.n.DisplayName < vj.n.DisplayName
	})

	// Render.
	fmt.Fprintln(w)
	writeSummaryLine(w, nodes)

	curGrp := groupID(-1)
	for _, v := range visible {
		if v.grp != curGrp {
			curGrp = v.grp
			g := planGroups[curGrp]
			fmt.Fprintln(w)
			sectionHeader(w, g.label, g.color, tw)
			fmt.Fprintln(w)
		}

		n := v.n
		color := actionColor(n.Action)
		name := truncatePath(n.DisplayName, maxNameLen)

		// Spaces so that type+level lands at the right edge.
		// right block visible length is fixed at rightBlockLen.
		gap := tw - prefixLen - len(name) - rightBlockLen
		if gap < minGap {
			gap = minGap
		}

		fmt.Fprintf(w, "  %s  %s%s%s%-*s  %s%s\n",
			color(itemSymbol(n.Action)),
			name,
			strings.Repeat(" ", gap),
			ansiDim,
			typeW, v.typ,
			v.level,
			ansiReset,
		)

		// Sub-lines: skip reason first, then description, then notes.
		if n.Action == ActionSkip && n.SkipReason != "" {
			for _, part := range strings.Split(n.SkipReason, "; ") {
				noteLine(w, part, tw)
			}
		}
		if n.Description != "" && n.Action != ActionSkip {
			noteLine(w, n.Description, tw)
		}
		for _, note := range n.Notes {
			noteLine(w, note, tw)
		}
	}

	// Bottom divider.
	fmt.Fprintln(w)
	divider(w, tw)
	fmt.Fprintln(w)
}

// ─── Execution display ────────────────────────────────────────────────────────

// execNameW returns the name column width for execution rows.
// Execution rows have no level column, so we reclaim that space from the
// plan-display nameW rather than re-querying termWidth (which may return a
// wrong value when running inside a distrobox or pipe).
// The level column is typically "common" (6) or "user_config" (11) + 2 gap = ~8.
const execLevelReclaim = 10 // chars reclaimed by dropping the level column

func execNameW() int {
	w := displayMetrics.nameW + execLevelReclaim
	if w < 10 {
		w = 10
	}
	return w
}

// DisplayExecutionProgress prints a pending-action line ending with \r so that
// DisplayExecutionResult can overwrite it in place. lastLine, if non-empty,
// is shown as a dim hint after the ellipsis so long-running commands look alive.
func DisplayExecutionProgress(w io.Writer, node *PlanNode, lastLine string) {
	tw := termWidth()
	nw := execNameW()
	name := truncatePath(node.DisplayName, nw)
	// Fixed portion: "  ·  <name padded>  <type padded>  …  "
	fixedLen := 2 + 1 + 2 + nw + 2 + displayMetrics.typeW + 4
	hint := ""
	if lastLine != "" {
		budget := tw - fixedLen - 1
		if budget > 8 {
			if len(lastLine) > budget {
				lastLine = lastLine[:budget-1] + "…"
			}
			hint = ansiDim + lastLine + ansiReset
		}
	}
	fmt.Fprintf(w, "\r\033[K  %s  %-*s  %-*s  %s…%s  %s",
		ansiDim+"·"+ansiReset,
		nw, name,
		displayMetrics.typeW, typeLabel(node.ResourceType),
		ansiDim, ansiReset,
		hint,
	)
}

// DisplayExecutionResult overwrites the progress line with the final outcome.
// Pass dur=0 for TRACK/ADOPT/LEVEL nodes and skipped-by-dependency nodes.
func DisplayExecutionResult(w io.Writer, node *PlanNode, err error, dur time.Duration) {
	nw := execNameW()
	name := truncatePath(node.DisplayName, nw)
	typ := typeLabel(node.ResourceType)

	var sym, status string
	switch {
	case err != nil:
		sym = ansiRed + "✗" + ansiReset
		status = ansiRed + "failed" + ansiReset
	case node.Action == ActionSkip:
		sym = ansiDim + "!" + ansiReset
		status = ansiDim + "skipped" + ansiReset
	default:
		sym = ansiGreen + "✓" + ansiReset
		if dur > 0 {
			status = ansiDim + formatDur(dur) + ansiReset
		}
	}

	fmt.Fprintf(w, "\r\033[K  %s  %-*s  %-*s  %s\n",
		sym,
		nw, name,
		displayMetrics.typeW, typ,
		status,
	)

	if err != nil {
		noteLine(w, err.Error(), termWidth())
	}
}

// ─── Confirmation prompt ──────────────────────────────────────────────────────

// Confirm prints the prompt and reads a Y/n response.
// Returns true if the user presses Enter or types y/Y/yes.
func Confirm(w io.Writer, r io.Reader) (bool, error) {
	fmt.Fprint(w, "  Proceed? [Y/n]: ")
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

// ─── Terminal helpers ─────────────────────────────────────────────────────────

// termWidth returns the current terminal width, or 120 as a fallback.
func termWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w < 40 {
		return 120
	}
	return w
}

// truncatePath shortens a path to at most width characters, preserving the
// start of the path and the last two segments (parent/file), joined by "/.../".
// If even the last two segments exceed the budget, the filename is truncated.
func truncatePath(s string, width int) string {
	if width <= 0 || len(s) <= width {
		return s
	}
	tail := s
	if i := strings.LastIndex(s, "/"); i > 0 {
		if j := strings.LastIndex(s[:i], "/"); j >= 0 {
			tail = s[j+1:]
		}
	}
	const ellipsis = "/.../"
	tailRoom := width - len(ellipsis)
	if tailRoom < 3 {
		if len(s) > width-1 {
			return s[:width-1] + "…"
		}
		return s
	}
	if len(tail) > tailRoom {
		tail = tail[:tailRoom-3] + "..."
	}
	frontRoom := width - len(ellipsis) - len(tail)
	if frontRoom <= 0 {
		return tail
	}
	return s[:frontRoom] + ellipsis + tail
}

// padCenter centers s within a field of the given width.
// Used by DisplayShow.
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
