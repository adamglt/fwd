package main

import (
	"os/exec"
)

func setup(targets []*Namespace) error {
	for _, ns := range targets {
		for _, svc := range ns.Services {
			cmd := exec.Command("ifconfig", "lo0", "alias", svc.addr)
			if _, err := cmd.Output(); err != nil {
				return err
			}
		}
	}
	return nil
}

func cleanup(targets []*Namespace) error {
	for _, ns := range targets {
		for _, svc := range ns.Services {
			cmd := exec.Command("ifconfig", "lo0", "-alias", svc.addr)
			if _, err := cmd.Output(); err != nil {
				return err
			}
		}
	}
	return nil
}
