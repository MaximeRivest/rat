package repl

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"
)

// pickerItem represents one cell in the picker grid.
type pickerItem struct {
	Lang     string
	Instance int
	Name     string // kernel name
	Running  bool
}

// pickerResult is what the picker returns.
type pickerResult struct {
	Lang     string
	Instance int
	Name     string // kernel name (empty for ghost/new)
	Quit     bool   // true if user pressed Escape
}

// pickerGrid builds the grid from items.
type pickerGrid struct {
	langs []string         // sorted language list
	cells map[string][]cell // lang → cells (instances + ghost)
}

type cell struct {
	instance int
	running  bool
	ghost    bool   // "+" slot for creating new instances
	name     string // kernel name (empty for ghost/unstarted)
}

func buildPickerGrid(items []pickerItem) *pickerGrid {
	g := &pickerGrid{
		cells: make(map[string][]cell),
	}

	langSet := make(map[string]bool)
	langCells := make(map[string][]cell)

	for _, item := range items {
		langSet[item.Lang] = true
		langCells[item.Lang] = append(langCells[item.Lang], cell{
			instance: item.Instance,
			running:  item.Running,
			name:     item.Name,
		})
	}

	// Sort cells by instance number.
	for lang := range langCells {
		sort.Slice(langCells[lang], func(i, j int) bool {
			return langCells[lang][i].instance < langCells[lang][j].instance
		})
	}

	// Add ghost "+" slot after last instance for each language.
	for lang := range langCells {
		hasRunning := false
		maxInst := 0
		for _, c := range langCells[lang] {
			if c.running {
				hasRunning = true
			}
			if c.instance > maxInst {
				maxInst = c.instance
			}
		}
		if hasRunning {
			langCells[lang] = append(langCells[lang], cell{
				instance: maxInst + 1,
				ghost:    true,
			})
		}
	}

	// Sort languages: running first, then alphabetical.
	langs := make([]string, 0, len(langSet))
	for lang := range langSet {
		langs = append(langs, lang)
	}
	sort.Slice(langs, func(i, j int) bool {
		iRun := hasRunningCell(langCells[langs[i]])
		jRun := hasRunningCell(langCells[langs[j]])
		if iRun != jRun {
			return iRun // running first
		}
		return langs[i] < langs[j]
	})

	g.langs = langs
	g.cells = langCells
	return g
}

func hasRunningCell(cells []cell) bool {
	for _, c := range cells {
		if c.running {
			return true
		}
	}
	return false
}

// showPicker displays the interactive picker.
// stopKernelFunc is set by the caller to stop a kernel by name.
var stopKernelFunc func(name string)

// SetStopKernelFunc sets the function used to stop kernels from the picker.
func SetStopKernelFunc(f func(name string)) {
	stopKernelFunc = f
}

// DiscoverProjects returns all projects with their kernel items, sorted by recency.
func DiscoverProjects() []ProjectInfo {
	allKernels, err := allKernelsFunc()
	if err != nil || len(allKernels) == 0 {
		return nil
	}

	running, _ := runningKernelsFunc()
	runningSet := make(map[string]bool)
	for _, k := range running {
		runningSet[k.Name] = true
	}

	// Group kernels by project (derived from Cwd).
	type projData struct {
		cwd      string
		items    []pickerItem
		lastUsed int64
		seen     map[string]bool
	}
	projects := make(map[string]*projData) // project name → data

	for _, k := range allKernels {
		// Derive project name from kernel name or cwd.
		projName := ""
		basePart := k.Name

		// Strip instance suffix.
		if dot := strings.LastIndex(basePart, "."); dot > 0 {
			rest := basePart[dot+1:]
			valid := len(rest) > 0
			for _, c := range rest {
				if c < '0' || c > '9' {
					valid = false
					break
				}
			}
			if valid {
				basePart = basePart[:dot]
			}
		}

		if at := strings.Index(basePart, "@"); at >= 0 {
			projName = basePart[at+1:]
		} else if k.Cwd != "" {
			// No @ — derive project from cwd.
			// First try matching an existing project by cwd.
			for pn, pd := range projects {
				if pd.cwd == k.Cwd {
					projName = pn
					break
				}
			}
			if projName == "" {
				// Derive from cwd basename.
				parts := strings.Split(k.Cwd, "/")
				for i := len(parts) - 1; i >= 0; i-- {
					if parts[i] != "" {
						projName = parts[i]
						break
					}
				}
			}
		} else {
			continue
		}

		if projName == "" {
			continue
		}

		pd, ok := projects[projName]
		if !ok {
			pd = &projData{cwd: k.Cwd, seen: make(map[string]bool)}
			projects[projName] = pd
		}

		// Parse instance.
		instance := 1
		if dot := strings.LastIndex(k.Name, "."); dot > 0 {
			rest := k.Name[dot+1:]
			n := 0
			valid := len(rest) > 0
			for _, c := range rest {
				if c < '0' || c > '9' {
					valid = false
					break
				}
				n = n*10 + int(c-'0')
			}
			if valid && n >= 2 {
				instance = n
			}
		}

		// Custom runtimes (no @) get their own row using their name.
		displayLang := k.Lang
		if !strings.Contains(k.Name, "@") {
			displayLang = k.Name
			// Strip instance suffix from display.
			if dot := strings.LastIndex(displayLang, "."); dot > 0 {
				rest := displayLang[dot+1:]
				valid := len(rest) > 0
				for _, c := range rest {
					if c < '0' || c > '9' {
						valid = false
						break
					}
				}
				if valid {
					displayLang = displayLang[:dot]
				}
			}
		}

		key := fmt.Sprintf("%s:%d", displayLang, instance)
		if pd.seen[key] {
			continue
		}
		pd.seen[key] = true

		pd.items = append(pd.items, pickerItem{
			Lang:     displayLang,
			Instance: instance,
			Name:     k.Name,
			Running:  runningSet[k.Name],
		})

		if k.Started > pd.lastUsed {
			pd.lastUsed = k.Started
		}
	}

	// Include saved runtimes (from `rat add`) that haven't been started.
	if allRuntimesFunc != nil {
		runtimes, err := allRuntimesFunc()
		if err == nil {
			for _, rt := range runtimes {
				// Derive project from name or cwd.
				projName := ""
				if at := strings.Index(rt.Name, "@"); at >= 0 {
					projName = rt.Name[at+1:]
				} else if rt.Cwd != "" {
					// Match to existing project by cwd.
					for pn, pd := range projects {
						if pd.cwd == rt.Cwd {
							projName = pn
							break
						}
					}
					// Still no match — derive from cwd basename.
					if projName == "" {
						parts := strings.Split(rt.Cwd, "/")
						for i := len(parts) - 1; i >= 0; i-- {
							if parts[i] != "" {
								projName = parts[i]
								break
							}
						}
					}
				}

				if projName == "" {
					continue
				}

				pd, ok := projects[projName]
				if !ok {
					pd = &projData{cwd: rt.Cwd, seen: make(map[string]bool)}
					projects[projName] = pd
				}

				key := fmt.Sprintf("%s:1", rt.Lang)
				if !pd.seen[key] {
					pd.seen[key] = true
					pd.items = append(pd.items, pickerItem{
						Lang:     rt.Lang,
						Instance: 1,
						Name:     rt.Name,
						Running:  runningSet[rt.Name],
					})
				}
			}
		}
	}

	// Convert to sorted list.
	var result []ProjectInfo
	for name, pd := range projects {
		// Add common languages.
		for _, lang := range []string{"py", "sh", "r"} {
			key := fmt.Sprintf("%s:1", lang)
			if !pd.seen[key] {
				pd.seen[key] = true
				pd.items = append(pd.items, pickerItem{
					Lang:     lang,
					Instance: 1,
					Running:  false,
				})
			}
		}
		result = append(result, ProjectInfo{
			Name:     name,
			Cwd:      pd.cwd,
			Items:    pd.items,
			LastUsed: pd.lastUsed,
		})
	}

	// Sort by recency (most recent first).
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastUsed > result[j].LastUsed
	})

	return result
}

// Context for rebuilding the grid after stop.
var discoverBaseName, discoverBaseCwd string

// ShowPicker displays the interactive picker with project cycling.
func ShowPicker(items []pickerItem, currentLang string, currentInstance int, currentName ...string) pickerResult {
	projects := DiscoverProjects()
	if len(projects) == 0 {
		// Fallback: single project from items.
		if len(items) == 0 {
			return pickerResult{Quit: true}
		}
		projects = []ProjectInfo{{Name: "", Items: items}}
	}

	// Determine the current kernel name for matching.
	curName := ""
	if len(currentName) > 0 {
		curName = currentName[0]
	}

	// Find the current project by matching kernel name.
	curProj := 0
	if curName != "" {
		for i, p := range projects {
			for _, item := range p.Items {
				if item.Name == curName {
					curProj = i
					break
				}
			}
		}
	} else if currentLang != "" {
		for i, p := range projects {
			for _, item := range p.Items {
				if item.Lang == currentLang {
					curProj = i
					break
				}
			}
		}
	}

	grid := buildPickerGrid(projects[curProj].Items)

	// Find initial cursor position by matching kernel name, then lang.
	curRow := 0
	curCol := 0
	found := false
	if curName != "" {
		for i, lang := range grid.langs {
			for j, c := range grid.cells[lang] {
				if c.name == curName {
					curRow = i
					curCol = j
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}
	if !found {
		for i, lang := range grid.langs {
			if lang == currentLang {
				curRow = i
				for j, c := range grid.cells[lang] {
					if c.instance == currentInstance {
						curCol = j
						break
					}
				}
				break
			}
		}
	}

	// Put terminal in raw mode.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return pickerResult{Lang: currentLang, Instance: currentInstance}
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	buf := make([]byte, 32)

	// Initial render.
	renderPicker(grid, curRow, curCol, projects, curProj, true)

	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return pickerResult{Quit: true}
		}

		switch {
		case buf[0] == 0x1b && n >= 3 && buf[1] == '[':
			switch buf[2] {
			case 'A': // Up
				curRow, curCol = moveRow(grid, curRow, curCol, -1)
			case 'B': // Down
				curRow, curCol = moveRow(grid, curRow, curCol, 1)
			case 'C': // Right
				curCol = moveCol(grid, curRow, curCol, 1)
			case 'D': // Left
				curCol = moveCol(grid, curRow, curCol, -1)
			case '3': // Delete key (ESC [ 3 ~)
				if n >= 4 && buf[3] == '~' {
					grid, curRow, curCol = handleStop(grid, curRow, curCol, projects, curProj)
				}
			}
		case buf[0] == 0x09: // Tab — next project
			if len(projects) > 1 {
				clearPickerArea(grid)
				curProj = (curProj + 1) % len(projects)
				grid = buildPickerGrid(projects[curProj].Items)
				curRow, curCol = 0, 0
				renderPicker(grid, curRow, curCol, projects, curProj, true)
				continue
			}
		case buf[0] == 0x1b && n == 2 && buf[1] == 0x09: // Shift-Tab (ESC Tab) — prev project
			if len(projects) > 1 {
				clearPickerArea(grid)
				curProj = (curProj - 1 + len(projects)) % len(projects)
				grid = buildPickerGrid(projects[curProj].Items)
				curRow, curCol = 0, 0
				renderPicker(grid, curRow, curCol, projects, curProj, true)
				continue
			}
		case buf[0] == 0x0d || buf[0] == 0x0a: // Enter
			lang := grid.langs[curRow]
			c := grid.cells[lang][curCol]
			clearPickerArea(grid)
			return pickerResult{Lang: lang, Instance: c.instance, Name: c.name}
		case buf[0] == 0x1b && n == 1: // Escape
			clearPickerArea(grid)
			return pickerResult{Quit: true}
		case buf[0] == 0x03 || buf[0] == 0x04: // Ctrl-C or Ctrl-D
			clearPickerArea(grid)
			return pickerResult{Quit: true}
		case buf[0] == 'q':
			clearPickerArea(grid)
			return pickerResult{Quit: true}
		case buf[0] == 'x' || buf[0] == 'd': // x or d to stop
			grid, curRow, curCol = handleStop(grid, curRow, curCol, projects, curProj)
		case buf[0] == 'j':
			curRow, curCol = moveRow(grid, curRow, curCol, 1)
		case buf[0] == 'k':
			curRow, curCol = moveRow(grid, curRow, curCol, -1)
		case buf[0] == 'l':
			curCol = moveCol(grid, curRow, curCol, 1)
		case buf[0] == 'h':
			curCol = moveCol(grid, curRow, curCol, -1)
		default:
			continue
		}

		renderPicker(grid, curRow, curCol, projects, curProj, false)
	}
}

// handleStop stops the kernel under cursor and rebuilds the grid.
func handleStop(grid *pickerGrid, curRow, curCol int, projects []ProjectInfo, curProj int) (*pickerGrid, int, int) {
	lang := grid.langs[curRow]
	c := grid.cells[lang][curCol]
	if !c.running || c.ghost || stopKernelFunc == nil {
		return grid, curRow, curCol
	}
	stopKernelFunc(c.name)
	// Rebuild from fresh data.
	newProjects := DiscoverProjects()
	if curProj < len(newProjects) {
		projects[curProj] = newProjects[curProj]
	}
	newItems := projects[curProj].Items
	grid = buildPickerGrid(newItems)
	if curRow >= len(grid.langs) {
		curRow = len(grid.langs) - 1
	}
	if curRow >= 0 && curCol >= len(grid.cells[grid.langs[curRow]]) {
		curCol = len(grid.cells[grid.langs[curRow]]) - 1
	}
	return grid, curRow, curCol
}

func moveRow(grid *pickerGrid, curRow, curCol, delta int) (int, int) {
	newRow := curRow + delta
	if newRow < 0 || newRow >= len(grid.langs) {
		return curRow, curCol
	}
	lang := grid.langs[newRow]
	if curCol >= len(grid.cells[lang]) {
		curCol = len(grid.cells[lang]) - 1
	}
	return newRow, curCol
}

func moveCol(grid *pickerGrid, curRow, curCol, delta int) int {
	lang := grid.langs[curRow]
	newCol := curCol + delta
	if newCol < 0 || newCol >= len(grid.cells[lang]) {
		return curCol
	}
	return newCol
}

// Display name mapping.
var langDisplayNames = map[string]string{
	"py": "python", "sh": "shell", "r": "r",
	"jl": "julia", "js": "node", "pi": "pi",
}

func langDisplay(lang string) string {
	if d, ok := langDisplayNames[lang]; ok {
		return d
	}
	return lang
}


func pickerTotalLines(grid *pickerGrid) int {
	// header + blank + legend_header + max(langs, 3 legend rows) + blank + actions
	gridRows := len(grid.langs)
	if gridRows < 3 {
		gridRows = 3 // legend needs at least 3 rows
	}
	return gridRows + 5 // header + blank + legend_header + grid + blank + actions
}

func renderPicker(grid *pickerGrid, curRow, curCol int, projects []ProjectInfo, curProj int, first bool) {
	dim := "\033[2m"
	bold := "\033[1m"
	cyan := "\033[36m"
	green := "\033[32m"
	reset := "\033[0m"
	sel := "\033[7m"
	dimSel := "\033[2;7m"

	const cellW = 4

	maxCols := 0
	for _, lang := range grid.langs {
		if n := len(grid.cells[lang]); n > maxCols {
			maxCols = n
		}
	}

	runningCount := 0
	for _, lang := range grid.langs {
		for _, c := range grid.cells[lang] {
			if c.running {
				runningCount++
			}
		}
	}

	// Legend entries to render alongside grid rows.
	type legendEntry struct {
		symbol string
		style  string
		label  string
	}
	legend := []legendEntry{
		{"1 2 \u2026", green + bold, "running"},
		{"    \u00b7", dim, "not running"},
		{"    +", dim, "add instance"},
	}

	// Fixed column for right-aligned legend.
	const legendCol = 50

	totalLines := pickerTotalLines(grid)

	if !first {
		fmt.Fprintf(os.Stdout, "\033[%dA\r", totalLines)
	}
	fmt.Fprint(os.Stdout, "\033[J")

	// Header.
	projName := ""
	if curProj < len(projects) {
		projName = projects[curProj].Name
	}
	noun := "kernels"
	if runningCount == 1 {
		noun = "kernel"
	}
	if projName != "" {
		if runningCount > 0 {
			fmt.Fprintf(os.Stdout, "  %s%d %s running%s in %s%s%s\r\n", green, runningCount, noun, reset, bold, projName, reset)
		} else {
			fmt.Fprintf(os.Stdout, "  %s%s%s %s(no kernels running)%s\r\n", bold, projName, reset, dim, reset)
		}
	} else {
		if runningCount > 0 {
			fmt.Fprintf(os.Stdout, "  %s%d %s running%s\r\n", green, runningCount, noun, reset)
		} else {
			fmt.Fprintf(os.Stdout, "  %sno kernels running%s\r\n", dim, reset)
		}
	}
	fmt.Fprint(os.Stdout, "\r\n")

	// Legend header row.
	fmt.Fprintf(os.Stdout, "\033[%dG%s%s\033[4m%s\033[0m%s\r\n", legendCol, dim, reset, dim+"legend", reset)

	// Grid + legend (legend rendered to the right of first 3 rows).
	gridRows := len(grid.langs)
	if gridRows < 3 {
		gridRows = 3
	}
	for i := 0; i < gridRows; i++ {
		var lang string
		var cells []cell
		var isCurrentRow bool
		if i < len(grid.langs) {
			lang = grid.langs[i]
			cells = grid.cells[lang]
			isCurrentRow = i == curRow
		}
		// Render grid row.
		rowLen := 0
		if lang != "" {
			display := langDisplay(lang)
			hasRunning := hasRunningCell(cells)

			if isCurrentRow {
				fmt.Fprintf(os.Stdout, "  %s%s%-10s%s", bold, cyan, display, reset)
			} else if hasRunning {
				fmt.Fprintf(os.Stdout, "  %-10s", display)
			} else {
				fmt.Fprintf(os.Stdout, "  %s%-10s%s", dim, display, reset)
			}
			rowLen = 12

			for j := 0; j < maxCols; j++ {
				if j >= len(cells) {
					fmt.Fprintf(os.Stdout, "%*s", cellW, "")
					rowLen += cellW
					continue
				}
				c := cells[j]
				isSelected := isCurrentRow && j == curCol

				var label string
				if c.ghost {
					label = "+"
				} else if c.running {
					label = fmt.Sprintf("%d", c.instance)
				} else {
					label = "\u00b7"
				}

				padded := fmt.Sprintf(" %-3s", label)

				if isSelected {
					if c.running {
						fmt.Fprintf(os.Stdout, "%s%s%s%s", sel, bold, padded, reset)
					} else {
						fmt.Fprintf(os.Stdout, "%s%s%s", dimSel, padded, reset)
					}
				} else if c.running {
					fmt.Fprintf(os.Stdout, "%s%s%s%s", green, bold, padded, reset)
				} else {
					fmt.Fprintf(os.Stdout, "%s%s%s", dim, padded, reset)
				}
				rowLen += cellW
			}
		} else {
			// Empty row (for legend alignment when grid has < 3 rows).
			rowLen = 0
		}

		// Render legend entry on the right (absolute column position).
		if i < len(legend) {
			e := legend[i]
			fmt.Fprintf(os.Stdout, "\033[%dG%s%s%s  %s%s%s", legendCol, e.style, e.symbol, reset, dim, e.label, reset)
		}

		fmt.Fprint(os.Stdout, "\r\n")
	}

	// Actions bar.
	fmt.Fprint(os.Stdout, "\r\n")
	actions := fmt.Sprintf("  %s\u2191\u2193\u2190\u2192 move   enter connect   esc terminal   x stop",  dim)
	if len(projects) > 1 {
		actions += fmt.Sprintf("   tab %d projects", len(projects))
	}
	actions += reset + "\r\n"
	fmt.Fprint(os.Stdout, actions)
}

func clearPickerArea(grid *pickerGrid) {
	totalLines := pickerTotalLines(grid)
	fmt.Fprintf(os.Stdout, "\033[%dA\r", totalLines)
	fmt.Fprint(os.Stdout, "\033[J")
}

// DiscoverPickerItems builds the picker grid from running and stopped kernels.
func DiscoverPickerItems(baseName string, baseCwd string) []pickerItem {
	// Store context for rebuild after stop.
	discoverBaseName = baseName
	discoverBaseCwd = baseCwd

	atIdx := strings.Index(baseName, "@")
	var projectSuffix string
	if atIdx >= 0 {
		projectSuffix = baseName[atIdx:]
	}

	kernels, err := runningKernelsFunc()
	if err != nil {
		return nil
	}

	allKernels, err := allKernelsFunc()
	if err != nil {
		allKernels = kernels
	}

	runningSet := make(map[string]bool)
	for _, k := range kernels {
		runningSet[k.Name] = true
	}

	seen := make(map[string]bool)
	var items []pickerItem

	for _, k := range allKernels {
		name := k.Name
		lang := k.Lang

		instance := 1
		basePart := name
		if dot := strings.LastIndex(name, "."); dot > 0 {
			rest := name[dot+1:]
			n := 0
			valid := len(rest) > 0
			for _, c := range rest {
				if c < '0' || c > '9' {
					valid = false
					break
				}
				n = n*10 + int(c-'0')
			}
			if valid && n >= 2 {
				instance = n
				basePart = name[:dot]
			}
		}

		if projectSuffix != "" {
			if !strings.HasSuffix(basePart, projectSuffix) && k.Cwd != baseCwd {
				continue
			}
		} else if baseCwd != "" {
			if k.Cwd != baseCwd {
				continue
			}
		}

		key := fmt.Sprintf("%s:%d", lang, instance)
		if seen[key] {
			continue
		}
		seen[key] = true

		items = append(items, pickerItem{
			Lang:     lang,
			Instance: instance,
			Name:     name,
			Running:  runningSet[name],
		})
	}

	commonLangs := []string{"py", "sh", "r"}
	for _, lang := range commonLangs {
		key := fmt.Sprintf("%s:1", lang)
		if !seen[key] {
			seen[key] = true
			items = append(items, pickerItem{
				Lang:     lang,
				Instance: 1,
				Running:  false,
			})
		}
	}

	return items
}

// KernelInfo is a minimal struct to decouple from state package.
type KernelInfo struct {
	Name    string
	Lang    string
	Cwd     string
	Started int64
}

// ProjectInfo groups picker items by project.
type ProjectInfo struct {
	Name     string
	Cwd      string
	Items    []pickerItem
	LastUsed int64
}

var runningKernelsFunc func() ([]KernelInfo, error)
var allKernelsFunc func() ([]KernelInfo, error)

type RuntimeInfo struct {
	Name string
	Lang string
	Cwd  string
}

var allRuntimesFunc func() ([]RuntimeInfo, error)

func SetRunningKernelsFunc(f func() ([]KernelInfo, error)) {
	runningKernelsFunc = f
}

func SetAllKernelsFunc(f func() ([]KernelInfo, error)) {
	allKernelsFunc = f
}

func SetAllRuntimesFunc(f func() ([]RuntimeInfo, error)) {
	allRuntimesFunc = f
}
