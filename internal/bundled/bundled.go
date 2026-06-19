package bundled

import (
	"embed"
	"io/fs"
)

//go:embed agents/*.md all:skills all:plugins
var assets embed.FS

func Agents() fs.FS {
	return mustSub("agents")
}

func Skills() fs.FS {
	return mustSub("skills")
}

func Plugins() fs.FS {
	return mustSub("plugins")
}

func mustSub(dir string) fs.FS {
	sub, err := fs.Sub(assets, dir)
	if err != nil {
		panic("invalid bundled asset directory " + dir + ": " + err.Error())
	}
	return sub
}
