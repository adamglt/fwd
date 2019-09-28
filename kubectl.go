package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type kubectl interface {
	// gets availble and current context
	contexts() ([]string, string, error)

	// gets all ports in a given context
	ports(kctx string) ([]port, error)

	// port-forward with machine-learning based error detection.
	// that or first stderr line, whatever happens first.
	forward(ctx context.Context, kctx string, ns string, svc string, ports []string, addr string) error
}

// k8s port entry
type port struct {
	namespace string
	service   string
	proto     string
	name      string
	number    string // no need for string->int->string
}

// kubectl exec proxy
type kubectlExec struct{}

func (kubectlExec) contexts() ([]string, string, error) {
	// get all contexts
	allCmd := exec.Command("kubectl", "config", "get-contexts", "-o", "name")
	allOut, err := allCmd.CombinedOutput()
	if err != nil {
		return nil, "", fmt.Errorf("get-contexts: %s", string(allOut))
	}

	// get current context
	curCmd := exec.Command("kubectl", "config", "current-context")
	curOut, err := curCmd.CombinedOutput()
	if err != nil {
		return nil, "", fmt.Errorf("current-context: %s", string(allOut))
	}

	return strings.Split(string(allOut), "\n"), string(curOut), nil
}

// output: <ns>,<svc>,<protocol>,<portName>,<portNumber>
const csvPorts = `{{range $i, $svc := .items}}{{range .spec.ports}}` +
	`{{$svc.metadata.namespace}},{{$svc.metadata.name}},{{.protocol}},{{.name}},{{.port}}` +
	`{{println}}{{end}}{{end}}`

func (kubectlExec) ports(kctx string) ([]port, error) {
	cmd := exec.Command("kubectl", "get", "services",
		"--context", kctx,
		"--all-namespaces",
		"-o=go-template="+csvPorts,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("get services: %w", err)
	}
	records, err := csv.NewReader(bytes.NewReader(out)).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to parse csv: %w", err)
	}
	var ports []port
	for _, record := range records {
		p := port{
			namespace: record[0],
			service:   record[1],
			proto:     strings.ToLower(record[2]), // protocol is shouting
			name:      record[3],
			number:    record[4],
		}
		if p.name == "<no value>" {
			p.name = "unnamed" // clearer log
		}
		ports = append(ports, p)
	}
	return ports, nil
}

var (
	errDone = errors.New("ctx.done")
)

func (kubectlExec) forward(ctx context.Context, kctx string, ns string, svc string, ports []string, addr string) error {
	cmd := exec.Command("kubectl", "port-forward",
		fmt.Sprintf("svc/%s", svc),
		"--context", kctx,
		"--address", addr,
		"--namespace", ns,
	)
	for _, port := range ports {
		cmd.Args = append(cmd.Args, port)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("pipe failed: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start failed: %w", err)
	}

	reset := make(chan struct{})
	exit := false
	go func() {
		select {
		case <-ctx.Done():
			// signal
			exit = true
		case <-reset:
			// shadow err
		}
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	// trigger kill on any stderr
	s := bufio.NewScanner(stderr)
	for s.Scan() {
		close(reset)
		break
	}

	// both don't really matter
	_ = s.Err()
	_ = cmd.Wait()

	// err on signal, otherwise hide
	if exit {
		return errDone
	}
	return nil
}
