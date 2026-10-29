// Command used detects whether a human is using this machine and reports it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/KilianSen/used/internal/config"
	"github.com/KilianSen/used/internal/daemon"
	"github.com/KilianSen/used/internal/install"
)

var version = "dev" // set by -ldflags "-X main.version=…"

type command struct {
	usage string
	run   func(cfg config.Config, args []string) error
}

var commands = map[string]command{
	"status":    {"[--json]                 current state (asks the daemon, else reads once)", cmdStatus},
	"watch":     {"[--json]                 follow state changes", cmdWatch},
	"explain":   {"                         why the machine is in its current state", cmdExplain},
	"daemon":    {"                         run in the foreground: probe, API, webhooks", cmdDaemon},
	"exec":      {"--tag T [--ttl D] -- cmd run cmd as a workload `used` ignores", cmdExec},
	"hold":      {"[--for D]                force `present` (I'm here, don't schedule)", override("hold")},
	"release":   {"[--for D] [--sticky]     force `unused` (I'm off, go ahead)", override("release")},
	"auto":      {"                         clear hold/release", override("none")},
	"tray":      {"[--background]           menu-bar / tray icon with hold and release", cmdTray},
	"install":   {"                         start the daemon at login (per user, no admin)", cmdInstall},
	"update":    {"[--check] [--version vX] update to the latest release, restart the daemon", cmdUpdate},
	"uninstall": {"                         stop the daemon and remove autostart", cmdUninstall},
	"config":    {"                         show config path and effective config", cmdConfig},
	"version":   {"                         print version", func(config.Config, []string) error { fmt.Println("used", version); return nil }},
}

var order = []string{"status", "watch", "explain", "exec", "hold", "release", "auto", "daemon", "tray", "install", "update", "uninstall", "config", "version"}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		usage()
		return
	}
	cmd, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "used: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	cfg, err := config.Load(config.Path())
	if err == nil {
		err = cmd.run(cfg, os.Args[2:])
	}
	if err != nil {
		var exit exitCode
		if errors.As(err, &exit) {
			os.Exit(int(exit))
		}
		fmt.Fprintln(os.Stderr, "used:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("used — is anyone using this machine?\n\nUsage:")
	for _, name := range order {
		fmt.Printf("  used %-9s %s\n", name, commands[name].usage)
	}
	fmt.Printf("\nConfig: %s\n", config.Path())
}

// exitCode lets a command exit with a specific status (used exec passes the child's through).
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func flags(name string) *flag.FlagSet { return flag.NewFlagSet("used "+name, flag.ExitOnError) }

func cmdStatus(cfg config.Config, args []string) error {
	fs := flags("status")
	asJSON := fs.Bool("json", false, "print JSON")
	_ = fs.Parse(args)

	c := dial(cfg)
	var ev daemon.Event
	if err := c.get("/v1/status", &ev); err == nil {
		return show(*asJSON, ev, func() { printStatus(ev.Presence, ev.Metrics, ev.Workloads) })
	}
	snap, err := oneShot(cfg)
	if err != nil {
		return err
	}
	return show(*asJSON, snap.Status, func() {
		printStatus(snap.Status, nil, nil)
		fmt.Println(dim("  (no daemon running: one-shot reading, no history or metrics)"))
	})
}

func cmdWatch(cfg config.Config, args []string) error {
	fs := flags("watch")
	asJSON := fs.Bool("json", false, "print raw NDJSON events")
	_ = fs.Parse(args)

	if err := dial(cfg).stream("/v1/events", func(line []byte) {
		if *asJSON {
			fmt.Println(string(line))
		} else {
			printEventLine(line)
		}
	}); !errors.Is(err, errNoDaemon) {
		return err
	}

	// No daemon: run the engine locally and print transitions.
	engine, _, err := daemon.Build(cfg)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, dim("no daemon running — watching locally (ctrl-c to stop)"))
	for {
		snap := engine.Tick()
		if snap.Changed {
			if *asJSON {
				printJSON(snap.Status)
			} else {
				printTransition(time.Now(), snap.Status)
			}
		}
		time.Sleep(time.Second)
	}
}

func cmdExplain(cfg config.Config, args []string) error {
	fs := flags("explain")
	asJSON := fs.Bool("json", false, "print JSON")
	_ = fs.Parse(args)

	var ex daemon.Explain
	if err := dial(cfg).get("/v1/explain", &ex); err != nil {
		snap, err := oneShot(cfg)
		if err != nil {
			return err
		}
		ex = daemon.Explain{Snapshot: snap, Rules: cfg.Ignore, Config: cfg.Presence}
	}
	return show(*asJSON, ex, func() { printExplain(ex) })
}

func cmdDaemon(cfg config.Config, args []string) error {
	fs := flags("daemon")
	logPath := fs.String("log", "", "append logs to this file instead of stderr")
	bg := fs.Bool("background", false, "detach from the console (Windows autostart)")
	_ = fs.Parse(args)
	if *bg {
		background()
	}
	out := os.Stderr
	if *logPath != "" {
		_ = os.MkdirAll(filepath.Dir(*logPath), 0o755)
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	log := slog.New(slog.NewTextHandler(out, nil))
	d, err := daemon.New(cfg, log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return d.Run(ctx)
}

func override(mode string) func(config.Config, []string) error {
	return func(cfg config.Config, args []string) error {
		fs := flags(mode)
		dur := fs.Duration("for", 0, "how long (default: until cleared)")
		sticky := fs.Bool("sticky", false, "release survives new input")
		_ = fs.Parse(args)

		req := daemon.OverrideRequest{Mode: mode, Sticky: *sticky}
		if *dur > 0 {
			req.For = dur.String()
		}
		var ev daemon.Event
		if err := dial(cfg).post("/v1/override", req, &ev); err != nil {
			return err
		}
		printStatus(ev.Presence, ev.Metrics, ev.Workloads)
		return nil
	}
}

func cmdInstall(cfg config.Config, _ []string) error { return reinstall(cfg) }

// reinstall registers autostart and (re)starts the daemon from this binary.
func reinstall(cfg config.Config) error {
	_ = dial(cfg).post("/v1/shutdown", nil, nil) // replace a running daemon (e.g. after upgrade)
	summary, err := install.Install()
	if err != nil {
		return err
	}
	fmt.Println(badge("active") + " installed\n  " + summary)
	return waitForDaemon(cfg)
}

func cmdUninstall(cfg config.Config, _ []string) error {
	summary, err := install.Uninstall()
	if err != nil {
		return err
	}
	_ = dial(cfg).post("/v1/shutdown", nil, nil)
	fmt.Println(summary)
	return nil
}

// waitForDaemon confirms the freshly registered daemon answers.
func waitForDaemon(cfg config.Config) error {
	c := dial(cfg)
	for range 50 {
		var ev daemon.Event
		if c.get("/v1/status", &ev) == nil {
			fmt.Println()
			printStatus(ev.Presence, ev.Metrics, ev.Workloads)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("installed, but the daemon did not come up within 5s; check the logs")
}

func cmdConfig(cfg config.Config, _ []string) error {
	fmt.Println("#", config.Path())
	fmt.Println("# socket:", cfg.SocketPath())
	fmt.Println()
	return toml.NewEncoder(os.Stdout).Encode(cfg)
}

// oneShot reads the machine once without a daemon.
func oneShot(cfg config.Config) (daemon.Snapshot, error) {
	engine, _, err := daemon.Build(cfg)
	if err != nil {
		return daemon.Snapshot{}, err
	}
	return engine.Tick(), nil
}
