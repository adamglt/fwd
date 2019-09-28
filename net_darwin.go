package main

import (
	"errors"
	"os/exec"
	"strings"
)

// loopback aliases must be set up for all forwarded ips
func prepareIPs(ips []string) error {
	var aliased []string
	for _, ip := range ips {
		cmd := exec.Command("ifconfig", "lo0", "alias", ip)
		if _, err := cmd.Output(); err != nil {
			_ = cleanupIPs(aliased) // revert what we can
			return err
		}
		aliased = append(aliased, ip)
	}
	return nil
}

// remove loopback aliases before exit
func cleanupIPs(ips []string) error {
	var msgs []string
	for _, ip := range ips {
		cmd := exec.Command("ifconfig", "lo0", "-alias", ip)
		if _, err := cmd.Output(); err != nil {
			msgs = append(msgs, err.Error()) // collect and continue
		}
	}
	if len(msgs) > 0 {
		return errors.New(strings.Join(msgs, "; "))
	}
	return nil
}
