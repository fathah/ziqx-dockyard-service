package buildinfo

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
)

// Version and Commit are set by release build flags. Development builds remain identifiable.
var Version = "dev"
var Commit = "unknown"

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Target  string `json:"target"`
}

func Current() Info {
	return Info{Version: Version, Commit: Commit, Target: runtime.GOOS + "/" + runtime.GOARCH}
}

func Print(w io.Writer, machine bool) error {
	info := Current()
	if machine {
		return json.NewEncoder(w).Encode(info)
	}
	_, err := fmt.Fprintf(w, "Dockyard %s (%s) %s\n", info.Version, info.Commit, info.Target)
	return err
}
