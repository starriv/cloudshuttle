// Package icons holds the app's icons: Tabler's outline icons (MIT), copied
// unchanged into svg/.
package icons

import (
	"embed"

	"github.com/egoist/mygo/ui"
)

//go:embed svg/*.svg
var files embed.FS

func load(name string) *ui.SVG {
	data, err := files.ReadFile("svg/" + name + ".svg")
	if err != nil {
		panic(err)
	}
	return ui.MustParseSVG(data)
}

var (
	Overview = load("layout-dashboard")
	Tasks    = load("arrows-exchange")
	Storages = load("database")
	Bucket   = load("cloud")
	Server   = load("server")
	Shares   = load("folders")
	Refresh  = load("refresh")
	Delete   = load("trash")
	History  = load("history")
	Add      = load("plus")
	Run      = load("player-play")
	Stop     = load("x")
	More     = load("dots")
	Folder   = load("folder")
	Test     = load("plug-connected")
	Success  = load("circle-check")
	Untested = load("circle-dashed")
	Idle     = load("circle")
	Failed   = load("circle-x")
	Stopped  = load("alert-triangle")
	Warning  = load("alert-triangle")
	Running  = load("loader-2")
	Settings = load("settings")
	Download = load("download")
)
