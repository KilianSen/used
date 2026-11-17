package main

import (
	"fmt"

	"github.com/KilianSen/used/internal/config"
	"github.com/KilianSen/used/internal/daemon"
	"github.com/KilianSen/used/internal/install"
	"github.com/KilianSen/used/internal/update"
)

func cmdUpdate(cfg config.Config, args []string) error {
	fs := flags("update")
	check := fs.Bool("check", false, "only report whether an update exists")
	want := fs.String("version", "", "install this release tag instead of the latest (also downgrades)")
	_ = fs.Parse(args)

	repo := update.Repo()
	tag := *want
	if tag == "" {
		var err error
		if tag, err = update.Latest(repo); err != nil {
			return err
		}
		switch {
		case version == "dev":
			if *check {
				fmt.Printf("latest release is %s (this is a development build)\n", tag)
				return nil
			}
			fmt.Printf("this is a development build; replacing it with %s\n", tag)
		case !update.Newer(tag, version):
			fmt.Printf("used %s is up to date (latest %s)\n", version, tag)
			return nil
		}
	}
	if *check {
		fmt.Printf("update available: %s → %s (run `used update`)\n", version, tag)
		return nil
	}

	exe, err := install.Executable()
	if err != nil {
		return err
	}
	fmt.Printf("downloading %s …\n", tag)
	bin, err := update.Download(repo, tag)
	if err != nil {
		return err
	}
	if err := update.Replace(exe, bin); err != nil {
		return err
	}
	fmt.Printf("%s updated %s → %s (%s)\n", badge("active"), version, tag, exe)

	switch {
	case install.Installed():
		return reinstall(cfg) // restarts the daemon from the new binary
	case dial(cfg).get("/v1/status", &daemon.Event{}) == nil:
		fmt.Println(dim("a daemon is running but wasn't started by `used install`; restart it to use the new version"))
	}
	return nil
}
