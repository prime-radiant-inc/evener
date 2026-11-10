package cilanes

import (
	"encoding/json"
	"fmt"
	"io"
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

func (p plan) mark(l lane, why string) {
	if _, ok := p.why[l.id()]; !ok {
		p.why[l.id()] = why
	}
}

func (p plan) markAll(why string) {
	for _, l := range catalog {
		p.mark(l, why)
	}
}

// planLanes decides which lanes a change to the changed paths runs. Each
// path is one of four kinds. A path that steers every lane, or that the
// rules below cannot place, runs every lane. A workspace package's Go
// source runs the Go source lanes and every lane that runs a package
// depending on it. A path inside a TypeScript tree runs the TypeScript
// lanes and every lane that runs a package whose source names that path,
// with that package's own dependents.
func planLanes(ws *workspace, changed []string) plan {
	p := plan{why: map[string]string{}}
	if len(changed) == 0 {
		p.markAll("(no changed paths)")
		return p
	}
	seeds := map[string]string{}
	seed := func(dir, why string) {
		if _, ok := seeds[dir]; !ok {
			seeds[dir] = why
		}
	}
	for _, file := range changed {
		tree := typeScriptTreeOf(file)
		pkgDir, isPackageSource := ws.packageSource(file)
		if steersEveryLane(file) ||
			(strings.HasSuffix(file, ".go") && !isPackageSource) ||
			(tree == "" && !isPackageSource) {
			p.markAll(file)
			continue
		}
		if isPackageSource {
			seed(pkgDir, file)
			for _, l := range catalog {
				if l.goSource {
					p.mark(l, file)
				}
			}
		}
		if tree != "" {
			for _, l := range catalog {
				if l.typeScript {
					p.mark(l, file)
				}
			}
			for _, reader := range ws.readersOf(file, tree) {
				seed(reader, file)
			}
		}
	}
	reached := ws.dependents(seeds)
	dirs := make([]string, 0, len(reached))
	for dir := range reached {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)
	for _, l := range catalog {
		if l.runsPackage == nil {
			continue
		}
		for _, dir := range dirs {
			if l.runsPackage(dir, ws.moduleOf(dir)) {
				p.mark(l, reached[dir])
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
	var lines []string
	add := func(name string, value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		lines = append(lines, name+"="+string(data))
		return nil
	}
	var matrixJobs []string
	for _, l := range catalog {
		if cellsRun[l.job] == 0 && !slices.Contains(skip, l.job) {
			skip = append(skip, l.job)
		}
		if l.cell != "" && !slices.Contains(matrixJobs, l.job) {
			matrixJobs = append(matrixJobs, l.job)
		}
	}
	if err := add("skip", skip); err != nil {
		return err
	}
	for _, job := range matrixJobs {
		cells := excluded[job]
		if cellsRun[job] == 0 || cells == nil {
			cells = []map[string]string{}
		}
		if err := add("exclude_"+strings.ReplaceAll(job, "-", "_"), cells); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, strings.Join(lines, "\n")+"\n")
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
