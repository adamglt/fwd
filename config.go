package main

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

const (
	// file name fwd looks for
	configFile = ".fwd.yaml"

	// default local cidr
	defaultCIDR = "127.0.11.0/24"
)

// config yaml structure
type config struct {
	// local cidr to use for aliased ips
	// defaults to defaultCIDR
	CIDR string `yaml:"cidr"`

	// context->namespace->service tree
	Contexts []struct {
		Name       string `yaml:"name"` // defaults to current-context
		Namespaces []struct {
			Name     string `yaml:"name"`
			Services []struct {
				Name string `yaml:"name"`
			} `yaml:"services"`
		} `yaml:"namespaces"`
	} `yaml:"contexts"`
}

// reads the config file and flattens the tree structure.
// local directory is tried first, then the user's home directory.
func readConfig(log *logrus.Logger, configPath string) (string, []*target, error) {
	expanded := os.ExpandEnv(configPath)
	log.Infof("using config at: %s", expanded)
	raw, err := ioutil.ReadFile(expanded)
	if err != nil {
		return "", nil, err
	}

	// unmarshal cfg
	var cfg config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return "", nil, err
	}

	// set default cidr
	if cfg.CIDR == "" {
		cfg.CIDR = defaultCIDR
	}

	// flatten structure
	var targets []*target
	for _, c := range cfg.Contexts {
		for _, ns := range c.Namespaces {
			for _, svc := range ns.Services {
				targets = append(targets, &target{
					context:   c.Name,
					namespace: ns.Name,
					service:   svc.Name,
					ports:     map[string]string{},
				})
			}
		}
	}
	return cfg.CIDR, targets, nil
}

// generate n ips in the given cidr
func generateIPs(cidr string, n int) ([]string, error) {
	cur, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	var ips []string
	for i := 0; i < n; i++ {
		for i := len(cur) - 1; i >= 0; i-- {
			cur[i]++
			if cur[i] != 0 {
				break
			}
		}
		if !ipNet.Contains(cur) {
			return nil, fmt.Errorf("CIDR overflow (%v allocated)", i)
		}
		ips = append(ips, cur.String())
	}
	return ips, nil
}

// target represents a forwarded service (all ports)
type target struct {
	context   string // k8s context name
	namespace string // k8s namespace name
	service   string // k8s service name

	addr     string            // assigned local ip
	ports    map[string]string // detected ports (number->name,proto)
	conflict bool              // cross-context name collision
}

// local is a unique id within a context
func (t target) local() string {
	return localID(t.namespace, t.service)
}

// global is a unique id across all contexts
func (t target) global() string {
	return globalID(t.context, t.namespace, t.service)
}

// unique id within a context
func localID(namespace, service string) string {
	return fmt.Sprintf("%s.%s", service, namespace)
}

// unique id across all contexts
func globalID(context, namespace, service string) string {
	return fmt.Sprintf("%s.%s.%s", service, namespace, context)
}
