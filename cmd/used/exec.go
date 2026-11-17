package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/KilianSen/used/internal/config"
	"github.com/KilianSen/used/internal/daemon"
	"github.com/KilianSen/used/internal/workload"
)

// cmdExec registers itself as a workload, then runs the command as a child,
// so the whole tree below this process is ignored as evidence and counted
// as workload load. Works inside containers when the socket is mounted.
func cmdExec(cfg config.Config, args []string) error {
	fs := flags("exec")
	tag := fs.String("tag", "", "workload tag reported in events")
	ttl := fs.Duration("ttl", 0, "lease lifetime (default: workloads.max_ttl)")
	strict := fs.Bool("strict", false, "fail instead of running unregistered when the daemon is down")
	var meta metaFlag
	fs.Var(&meta, "meta", "k=v metadata, repeatable")
	_ = fs.Parse(args)
	argv := fs.Args()
	if len(argv) == 0 {
		return errors.New("usage: used exec --tag T [--ttl D] -- cmd [args…]")
	}

	req := daemon.LeaseRequest{Tag: *tag, Meta: meta}
	if runtime.GOOS == "windows" {
		req.PID = os.Getpid() // no peer credentials on Windows AF_UNIX
	}
	if *ttl > 0 {
		req.TTL = ttl.String()
	}
	c := dial(cfg)
	var lease workload.Lease
	if err := c.post("/v1/workloads", req, &lease); err != nil {
		if *strict {
			return err
		}
		fmt.Fprintln(os.Stderr, "used: warning:", err, "— running unregistered")
	} else {
		defer c.delete("/v1/workloads/" + lease.ID)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, forwardSignals...)
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()

	err := cmd.Wait()
	signal.Stop(sigs)
	time.Sleep(200 * time.Millisecond) // let the daemon attribute trailing children before the lease ends

	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code := ee.ExitCode()
		if code < 0 {
			code = 1
		}
		return exitCode(code)
	}
	return err
}

type metaFlag map[string]string

func (m *metaFlag) String() string { return fmt.Sprint(map[string]string(*m)) }
func (m *metaFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return errors.New("want k=v")
	}
	if *m == nil {
		*m = map[string]string{}
	}
	(*m)[k] = val
	return nil
}
