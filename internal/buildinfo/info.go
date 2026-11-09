package buildinfo

// Set by the release build. Direct Go builds retain these development values.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

type Info struct {
	Component string `json:"component"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
}

func Current(component string) Info {
	return Info{Component: component, Version: Version, Commit: Commit, BuildTime: BuildTime}
}
