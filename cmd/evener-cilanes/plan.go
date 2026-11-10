package cilanes

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
)

// plan is the set of lanes a change runs. A lane runs unless the plan can
// show that nothing it builds or tests depends on the change.
type plan struct {
	// why maps each lane that runs to a changed path that runs it.
	why map[string]string
}

func (p plan) runs(id string) bool {
	_, ok := p.why[id]
	return ok
}

// markWhere runs every lane that matches, crediting why.
func (p plan) markWhere(matches func(lane) bool, why string) {
	for _, l := range catalog {
		if matches(l) {
			setIfAbsent(p.why, l.id(), why)
		}
	}
}

func everyLane(lane) bool { return true }

// setIfAbsent keeps the first reason recorded for a key.
func setIfAbsent(m map[string]string, key, value string) {
	if _, ok := m[key]; !ok {
		m[key] = value
	}
}

// runsEverything reports the first changed path that runs every lane
// without consulting the workspace, so the caller can skip loading it: a
// path that steers every lane, or no changed path at all.
func runsEverything(changed []string) (string, bool) {
	if len(changed) == 0 {
		return "(no changed paths)", true
	}
	if i := slices.IndexFunc(changed, steersEveryLane); i >= 0 {
		return changed[i], true
	}
	return "", false
}

func allLanes(why string) plan {
	p := plan{why: map[string]string{}}
	p.markWhere(everyLane, why)
	return p
}

// planLanes decides which lanes a change to the changed paths runs. Each
// path is one of four kinds. A path that steers every lane, or that the
// rules below cannot place, runs every lane. A workspace package's Go
// source runs the Go source lanes and every lane that runs a package
// depending on it. A path inside a TypeScript tree runs the TypeScript
// lanes and every lane that runs a package whose source names that path,
// with that package's own dependents.
func planLanes(ws *workspace, changed []string) plan {
	if why, ok := runsEverything(changed); ok {
		return allLanes(why)
	}
	p := plan{why: map[string]string{}}
	buildSeeds, testSeeds := map[string]string{}, map[string]string{}
	for _, file := range changed {
		tree := typeScriptTreeOf(file)
		pkgDir, isPackageSource := ws.packageSource(file)
		if (strings.HasSuffix(file, ".go") && !isPackageSource) || (tree == "" && !isPackageSource) {
			p.markWhere(everyLane, file)
			continue
		}
		if isPackageSource {
			if strings.HasSuffix(file, "_test.go") {
				setIfAbsent(testSeeds, pkgDir, file)
			} else {
				setIfAbsent(buildSeeds, pkgDir, file)
			}
			p.markWhere(func(l lane) bool { return l.goSource }, file)
			if ws.goReadByTypeScript[file] {
				p.markWhere(func(l lane) bool { return l.typeScript }, file)
			}
		}
		if tree != "" {
			p.markWhere(func(l lane) bool { return l.typeScript }, file)
			build, test := ws.readersOf(file)
			for _, reader := range build {
				setIfAbsent(buildSeeds, reader, file)
			}
			for _, reader := range test {
				setIfAbsent(testSeeds, reader, file)
			}
		}
	}
	for dir := range ws.wholeTreeTesters {
		setIfAbsent(testSeeds, dir, changed[0])
	}
	reached := ws.affected(buildSeeds, testSeeds)
	dirs := slices.Sorted(maps.Keys(reached))
	for _, l := range catalog {
		if l.runsPackage == nil {
			continue
		}
		for _, dir := range dirs {
			if l.runsPackage(dir, ws.moduleOf(dir)) {
				setIfAbsent(p.why, l.id(), reached[dir])
				break
			}
		}
	}
	return p
}

func steersEveryLane(file string) bool {
	if slices.Contains(everyLaneBaseNames, path.Base(file)) {
		return true
	}
	return slices.ContainsFunc(everyLanePaths, func(p string) bool { return atOrUnder(file, p) })
}

func typeScriptTreeOf(file string) string {
	for _, tree := range typeScriptTrees {
		if atOrUnder(file, tree) {
			return tree
		}
	}
	return ""
}

// writeOutputs writes the plan as GitHub step outputs. skip lists the jobs
// none of whose lanes run; a job that runs some of its matrix cells is not
// in skip, and its exclude_<job> output lists the cells it skips, as
// matrix.exclude entries.
func (p plan) writeOutputs(w io.Writer) error {
	skip := []string{}
	cellsRun := map[string]int{}
	excluded := map[string][]map[string]string{}
	for _, l := range catalog {
		if p.runs(l.id()) {
			cellsRun[l.job]++
		} else if l.cell != "" {
			excluded[l.job] = append(excluded[l.job], map[string]string{matrixKeys[l.job]: l.cell})
		}
	}
	for _, l := range catalog {
		if cellsRun[l.job] == 0 && !slices.Contains(skip, l.job) {
			skip = append(skip, l.job)
		}
	}
	// json.Marshal cannot fail on string slices and string maps.
	var out strings.Builder
	line := func(name string, value any) {
		data, _ := json.Marshal(value)
		out.WriteString(name + "=" + string(data) + "\n")
	}
	line("skip", skip)
	for _, job := range slices.Sorted(maps.Keys(matrixKeys)) {
		cells := excluded[job]
		if cellsRun[job] == 0 || cells == nil {
			cells = []map[string]string{}
		}
		line("exclude_"+strings.ReplaceAll(job, "-", "_"), cells)
	}
	_, err := io.WriteString(w, out.String())
	return err
}

// writeReport lists each lane with its verdict and, for a lane that runs,
// the changed path that runs it.
func (p plan) writeReport(w io.Writer) error {
	for _, l := range catalog {
		verdict := "skip"
		if p.runs(l.id()) {
			verdict = "run "
		}
		if _, err := fmt.Fprintf(w, "%s %-28s %s\n", verdict, l.id(), p.why[l.id()]); err != nil {
			return err
		}
	}
	return nil
}
