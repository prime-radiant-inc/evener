package hub

import "flag"

// setFlagNames reports which flags of a parsed flag set the operator named, so
// a path or address they gave is distinguishable from its implicit default.
func setFlagNames(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}
