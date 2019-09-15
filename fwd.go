package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/txn2/txeh"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v2"
)

type Namespace struct {
	Name     string     `yaml:"name"`
	Services []*Service `yaml:"services"`
}

type Service struct {
	Name  string   `yaml:"name"`
	Ports []string `yaml:"ports"`
	ns    string
	key   string
	addr  string
}

func main() {
	if os.Geteuid() != 0 {
		fmt.Println("must run as root")
		os.Exit(1)
	}

	targets, err := readConfig()
	if err != nil {
		fmt.Printf("failed to read config file: %v\n", err)
		os.Exit(1)
	}
	if err := fill(targets); err != nil {
		fmt.Printf("failed to find services: %v\n", err)
		os.Exit(1)
	}

	hosts, err := txeh.NewHostsDefault()
	if err != nil {
		fmt.Printf("failed to init hosts: %v\n", err)
		os.Exit(1)
	}
	for _, ns := range targets {
		for _, svc := range ns.Services {
			hosts.RemoveAddress(svc.addr)
			hosts.AddHost(svc.addr, svc.key)
		}
	}
	fmt.Println("writing hosts...")
	if err := hosts.Save(); err != nil {
		fmt.Printf("failed to update hosts file: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	go watchSig(cancel)

	eg, ctx := errgroup.WithContext(ctx)
	for _, ns := range targets {
		for _, svc := range ns.Services {
			f := fwder{ctx, *svc}
			eg.Go(f.run)
		}
	}
	if err := eg.Wait(); err != nil {
		fmt.Printf("error caught: %v\n", err)
	}

	for _, ns := range targets {
		for _, svc := range ns.Services {
			hosts.RemoveAddress(svc.addr)
		}
	}
	fmt.Println("cleaning up hosts...")
	if err := hosts.Save(); err != nil {
		fmt.Printf("failed to update hosts file: %v\n", err)
		os.Exit(1)
	}
}

func readConfig() ([]*Namespace, error) {
	bb, err := ioutil.ReadFile("./.fwd.yaml")
	if err != nil && os.IsNotExist(err) {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return nil, herr
		}
		bb, err = ioutil.ReadFile(home + "/.fwd.yaml")
	}
	if err != nil {
		return nil, err
	}

	var cfg struct {
		Namespaces []*Namespace `yaml:"namespaces"`
	}
	if err := yaml.Unmarshal(bb, &cfg); err != nil {
		return nil, err
	}
	return cfg.Namespaces, nil
}

// output: <ns> <svc> <port...>
const tmpl = `{{range .items}}` +
	`{{.metadata.namespace}} {{.metadata.name}} ` +
	`{{range .spec.ports}}{{if eq .protocol "TCP"}}{{print .port " "}}{{end}}{{end}}` +
	`{{println}}{{end}}`

func fill(targets []*Namespace) error {
	autofill := make(map[string]*Service)
	idx := 0
	for _, ns := range targets {
		for _, svc := range ns.Services {
			idx++
			svc.ns = ns.Name
			svc.addr = fmt.Sprintf("127.0.11.%v", idx)
			svc.key = fmt.Sprintf("%s.%s", svc.Name, ns.Name)
			if len(svc.Ports) == 0 {
				autofill[svc.key] = svc
			}
		}
	}
	if len(autofill) == 0 {
		return nil
	}

	fmt.Println("filling service ports...")

	cmd := exec.Command("kubectl", "get", "services",
		"--all-namespaces",
		"-o=go-template="+tmpl)
	out, err := cmd.Output()
	if err != nil {
		return err
	}

	s := bufio.NewScanner(bytes.NewReader(out))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		segments := strings.Split(line, " ")
		if len(segments) < 3 {
			continue
		}
		key := fmt.Sprintf("%s.%s", segments[1], segments[0])
		if svc, ok := autofill[key]; ok {
			for _, port := range segments[2:] {
				svc.Ports = append(svc.Ports, port)
			}
		}
	}
	if err := s.Err(); err != nil {
		return err
	}

	var missing []string
	for _, ns := range targets {
		for _, svc := range ns.Services {
			if len(svc.Ports) == 0 {
				missing = append(missing, svc.key)
			}
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
	ctx context.Context
	svc Service
}

func (f fwder) run() error {
	// let all go routines catch up
	time.Sleep(100 * time.Millisecond)

	// run port-forward
	cmd := exec.Command("kubectl", "port-forward",
		fmt.Sprintf("svc/%s", f.svc.Name),
		"--address", f.svc.addr,
		"--namespace", f.svc.ns,
	)
	for _, port := range f.svc.Ports {
		cmd.Args = append(cmd.Args, port)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	go func() {
		<-f.ctx.Done()
		if cmd != nil && cmd.Process != nil {
			fmt.Printf("%s cleanup\n", f.svc.key)
			_ = cmd.Process.Kill()
		}
	}()

	if err := cmd.Run(); err != nil {
		cmd = nil
		return err
	}
	return nil
}
