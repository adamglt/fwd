package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/txn2/txeh"
	"golang.org/x/sync/errgroup"
)

var (
	log = &logrus.Logger{
		Out:       os.Stdout,
		Formatter: &logrus.TextFormatter{},
		Hooks:     make(logrus.LevelHooks),
		Level:     logrus.InfoLevel,
	}
)

func main() {
	if os.Geteuid() != 0 {
		fmt.Println("fwd must run as root")
		os.Exit(1)
	}

	targets, err := readConfig()
	if err != nil {
		log.Fatalf("failed to read config file: %v", err)
	}

	log.Info("filling contexts...")
	if err := fillContexts(targets); err != nil {
		log.Fatalf("failed to fill contexts: %v", err)
	}

	log.Info("checking for conflicts...")
	if err := markConflicts(targets); err != nil {
		log.Fatal(err)
	}

	log.Info("filling service ports...")
	if err := fillPorts(targets); err != nil {
		log.Fatalf("failed to fill ports: %v", err)
	}

	log.Info("running global setup...")
	if err := setup(targets); err != nil {
		log.Fatalf("failed to setup local network: %v", err)
	}

	log.Info("writing hosts...")
	hosts, err := txeh.NewHostsDefault()
	if err != nil {
		log.Fatalf("failed to init hosts: %v", err)
	}
	for _, t := range targets {
		hosts.RemoveAddress(t.addr)
		hosts.AddHost(t.addr, t.fqn())
		if !t.conflict {
			hosts.AddHost(t.addr, t.short())
		}
	}
	if err := hosts.Save(); err != nil {
		log.Fatalf("failed to update hosts file: %v", err)
	}

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	go watchSig(cancel)

	eg, ctx := errgroup.WithContext(ctx)
	for _, t := range targets {
		f := fwder{
			ctx:    ctx,
			target: t,
		}
		eg.Go(f.run)
	}
	if err := eg.Wait(); err != nil {
		log.Warnf("error caught: %v", err)
	}

	log.Info("cleaning up hosts...")
	for _, t := range targets {
		hosts.RemoveAddress(t.addr)
	}
	if err := hosts.Save(); err != nil {
		log.Fatalf("failed to update hosts file: %v", err)
	}

	log.Info("running global cleanup...")
	if err := cleanup(targets); err != nil {
		log.Fatalf("failed to cleanup local network: %v", err)
	}
}

func fillContexts(targets []*target) error {
	// find current context
	cmd := exec.Command("kubectl", "config", "current-context")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New(string(out))
	}
	cur := string(out)

	// fill missing entries
	for _, t := range targets {
		if t.context == "" {
			t.context = cur
		}
	}

	return nil
}

func markConflicts(targets []*target) error {
	shorts := make(map[string]*target, len(targets))
	dups := make(map[string]bool)
	for _, t := range targets {
		short := t.short()
		if other, ok := shorts[short]; ok {
			t.conflict = true
			other.conflict = true
			if t.context == other.context {
				dups[t.context] = true
			}
		} else {
			shorts[short] = t
		}
	}
	if len(dups) > 0 {
		var col []string
		for d := range dups {
			col = append(col, d)
		}
		return fmt.Errorf("duplicate service entries: %s", strings.Join(col, ", "))
	}
	return nil
}

// output: <svc>.<ns>,<protocol>,<portName>,<portNumber>
const tmpl = `{{range $i, $svc := .items}}{{range .spec.ports}}` +
	`{{$svc.metadata.name}}.{{$svc.metadata.namespace}},{{.protocol}},{{.name}},{{.port}}` +
	`{{println}}{{end}}{{end}}`

func fillPorts(targets []*target) error {
	var (
		contexts  []string                                 // unique contexts
		contextsM = make(map[string]bool)                  // dedup contexts
		targetsM  = make(map[string]*target, len(targets)) // targets by fqn
	)
	for _, t := range targets {
		if !contextsM[t.context] {
			contextsM[t.context] = true
			contexts = append(contexts, t.context)
		}
		targetsM[t.fqn()] = t
	}

	eg := errgroup.Group{}
	for _, c := range contexts {
		localC := c
		eg.Go(func() error {
			cmd := exec.Command("kubectl", "get", "services",
				"--context", localC,
				"--all-namespaces",
				"-o=go-template="+tmpl,
			)
			out, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("failed to run cmd: %w", err)
			}
			records, err := csv.NewReader(bytes.NewReader(out)).ReadAll()
			if err != nil {
				return fmt.Errorf("failed to parse csv: %w", err)
			}
			for _, record := range records {
				var (
					shortKey = record[0]
					proto    = record[1]
					name     = record[2]
					number   = record[3]
				)
				if proto != "TCP" {
					continue
				}
				if name == "<no value>" {
					name = "unnamed"
				}
				longKey := fmt.Sprintf("%s.%s", shortKey, localC)
				if t, ok := targetsM[longKey]; ok {
					t.ports[name] = number
				}
			}

			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}

	var missing []string
	for _, t := range targets {
		if len(t.ports) == 0 {
			missing = append(missing, t.fqn())
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("could not find service ports: %s", strings.Join(missing, ", "))
	}

	return nil
}

func watchSig(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	sig := <-ch
	fmt.Printf("caught [%s], exiting...\n", sig)
	cancel()
}

type fwder struct {
	ctx    context.Context
	target *target
}

func (f fwder) run() error {
	for {
		cmd := exec.Command("kubectl", "port-forward",
			fmt.Sprintf("svc/%s", f.target.service),
			"--context", f.target.context,
			"--address", f.target.addr,
			"--namespace", f.target.namespace,
		)
		for name, number := range f.target.ports {
			log.Infof("forwarding %s:%s (%s)", f.target.fqn(), number, name)
			if !f.target.conflict {
				log.Infof("forwarding %s:%s (%s)", f.target.short(), number, name)
			}
			cmd.Args = append(cmd.Args, number)
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return fmt.Errorf("pipe failed: %v", err)
		}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start failed: %v", err)
		}

		reset := make(chan struct{})
		exit := false
		go func() {
			select {
			case <-f.ctx.Done():
				// external signal
				exit = true
			case <-reset:
				// reset
			}
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}()

		s := bufio.NewScanner(stderr)
		for s.Scan() {
			log.Warnf("error detected in %s, reconnecting...", f.target.fqn())
			close(reset)
		}
		if err := s.Err(); err != nil {
			log.Warnf("scanner failed: %v", err)
		}

		_ = cmd.Wait()
		if exit {
			break
		}
	}

	return nil
}
