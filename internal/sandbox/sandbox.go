package sandbox

import (
	"os/exec"
	"runtime"
	"strings"
)

type WrapOpts struct {
	WorktreeDir      string
	ReadDirs         []string
	WriteDirs        []string
	Features         []string
	EnvKeys          []string
	AppendProfiles   []string
	SafehouseCommand string
}

func Wrap(command string, args []string, opts WrapOpts) (string, []string) {
	safehouse := opts.SafehouseCommand
	if safehouse == "" {
		safehouse = "safehouse"
	}

	var wrapped []string

	wrapped = append(wrapped, "--workdir", opts.WorktreeDir)

	if len(opts.ReadDirs) > 0 {
		wrapped = append(wrapped, "--add-dirs-ro", strings.Join(opts.ReadDirs, ":"))
	}

	if len(opts.WriteDirs) > 0 {
		wrapped = append(wrapped, "--add-dirs", strings.Join(opts.WriteDirs, ":"))
	}

	if len(opts.Features) > 0 {
		wrapped = append(wrapped, "--enable", strings.Join(opts.Features, ","))
	}

	if len(opts.EnvKeys) > 0 {
		wrapped = append(wrapped, "--env-pass", strings.Join(opts.EnvKeys, ","))
	}

	for _, profile := range opts.AppendProfiles {
		wrapped = append(wrapped, "--append-profile", profile)
	}

	wrapped = append(wrapped, "--", command)
	wrapped = append(wrapped, args...)

	return safehouse, wrapped
}

func Available() bool {
	return AvailableCommand("safehouse")
}

func AvailableCommand(command string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath(command)
	return err == nil
}
