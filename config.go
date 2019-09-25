package main

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"
)

const (
	configName = ".fwd.yaml"
)

type config struct {
	CIDR     string `yaml:"cidr"`
	Contexts []struct {
		Name       string `yaml:"name"`
		Namespaces []struct {
			Name     string `yaml:"name"`
			Services []struct {
				Name string `yaml:"name"`
			} `yaml:"services"`
		} `yaml:"namespaces"`
	} `yaml:"contexts"`
}

type target struct {
	context   string
	namespace string
	service   string
	addr      string
	ports     map[string]string
	conflict  bool
}

func (t target) short() string {
	return fmt.Sprintf("%s.%s", t.service, t.namespace)
}

func (t target) fqn() string {
	return fmt.Sprintf("%s.%s.%s", t.service, t.namespace, t.context)
}

func readConfig() ([]*target, error) {
	raw, err := ioutil.ReadFile(configName)
	if err != nil && os.IsNotExist(err) {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return nil, homeErr
		}
		raw, err = ioutil.ReadFile(filepath.Join(home, configName))
	}
	if err != nil {
		return nil, err
	}

	var cfg config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}

	alloc, err := newAllocator(cfg.CIDR)
	if err != nil {
		return nil, err
	}
	var targets []*target
	keys := make(map[string]*target)
	for _, c := range cfg.Contexts {
		for _, ns := range c.Namespaces {
			for _, svc := range ns.Services {
				addr, err := alloc.nextIP()
				if err != nil {
					return nil, err
				}
				t := &target{
					context:   c.Name,
					namespace: ns.Name,
					service:   svc.Name,
					addr:      addr,
					ports:     map[string]string{},
				}
				if other, ok := keys[t.short()]; ok {
					t.conflict = true
					other.conflict = true
				}
				targets = append(targets, t)
			}
		}
	}
	return targets, nil
}

type allocator struct {
	cur   net.IP
	net   *net.IPNet
	count int
}

func newAllocator(cidr string) (*allocator, error) {
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	return &allocator{
		cur:   ip,
		net:   ipNet,
		count: 0,
	}, nil
}

func (a *allocator) nextIP() (string, error) {
	for i := len(a.cur) - 1; i >= 0; i-- {
		a.cur[i]++
		if a.cur[i] != 0 {
			break
		}
	}
	if !a.net.Contains(a.cur) {
		return "", fmt.Errorf("CIDR overflow (%v allocated)", a.count)
	}
	a.count++
	return a.cur.String(), nil
}
