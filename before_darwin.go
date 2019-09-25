package main

import (
	"os/exec"
)

func setup(targets []*target) error {
	for _, t := range targets {
		cmd := exec.Command("ifconfig", "lo0", "alias", t.addr)
		if _, err := cmd.Output(); err != nil {
			return err
		}
	}
	return nil
}

func cleanup(targets []*target) error {
	for _, t := range targets {
		cmd := exec.Command("ifconfig", "lo0", "-alias", t.addr)
		if _, err := cmd.Output(); err != nil {
			return err
		}
	}
	return nil
}
