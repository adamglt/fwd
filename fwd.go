package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/txn2/txeh"
	"golang.org/x/sync/errgroup"
)

type fwd struct {
	log      logrus.FieldLogger
	kubectl  kubectl
	cidr     string
	targets  []*target
	contexts []string
}

func (f *fwd) run(ctx context.Context) error {
	f.log.Info("filling contexts...")
	if err := f.fillContexts(); err != nil {
		return fmt.Errorf("failed to fill target contexts: %w", err)
	}
	if err := f.checkConflicts(); err != nil {
		return fmt.Errorf("unresolveable conflicts: %w", err)
	}

	f.log.Info("filling service ports...")
	if missing, err := f.fillPorts(); err != nil {
		return fmt.Errorf("failed to fill ports: %w", err)
	} else if missing != nil {
		for _, m := range missing {
			f.log.Warnf("service not found: %s", m)
		}
	}

	ips, err := generateIPs(f.cidr, len(f.targets))
	if err != nil {
		return fmt.Errorf("failed to generate ips: %w", err)
	}

	f.log.Info("preparing local network...")
	if err := prepareIPs(ips); err != nil {
		return fmt.Errorf("failed to prepare local network: %w", err)
	}
	defer func() {
		f.log.Info("cleaning up local network...")
		if err := cleanupIPs(ips); err != nil {
			f.log.Warnf("failed to cleanup local network: %v", err)
		}
	}()

	f.log.Info("writing local hosts...")
	hosts, err := txeh.NewHostsDefault()
	if err != nil {
		return fmt.Errorf("failed to init hosts: %w", err)
	}
	for i := 0; i < len(f.targets); i++ {
		t := f.targets[i]
		t.addr = ips[i]                   // set addr (ips have same length)
		hosts.RemoveAddress(t.addr)       // remove old entries
		hosts.AddHost(t.addr, t.global()) // add global fwd
		if !t.conflict {
			hosts.AddHost(t.addr, t.local()) // add local when unique
		}
		for _, alias := range t.aliases {
			hosts.AddHost(t.addr, alias) // add aliases (globally unique)
		}
	}
	if err := hosts.Save(); err != nil {
		return fmt.Errorf("failed to write hosts file: %v", err)
	}
	defer func() {
		f.log.Info("cleaning up hosts...")
		for _, ip := range ips {
			hosts.RemoveAddress(ip)
		}
		if err := hosts.Save(); err != nil {
			f.log.Warnf("failed to cleanup hosts: %v", err)
		}
	}()

	eg, ctx := errgroup.WithContext(ctx)
	for _, t := range f.targets {
		eg.Go(f.child(ctx, t))
	}
	if err := eg.Wait(); err != nil {
		return fmt.Errorf("error caught: %w", err)
	}

	return nil
}

// fills empty contexts and validates all contexts referenced in the config
func (f *fwd) fillContexts() error {
	available, cur, err := f.kubectl.contexts()
	if err != nil {
		return fmt.Errorf("failed to retrieve kubectl contexts: %w", err)
	}

	used := make(map[string]bool, len(available))
	for _, kctx := range available {
		used[kctx] = false // insert as unused
	}

	for _, t := range f.targets {
		// try defaulting
		if t.context == "" {
			if cur == "" {
				return errors.New("config contains empty context but no default is available")
			}
			t.context = cur
		}
		// check available
		if _, ok := used[t.context]; !ok {
			return fmt.Errorf("unknown config context: %s", t.context)
		}
		// set as used
		used[t.context] = true
	}

	// collect contexts referenced by the config
	for name, used := range used {
		if used {
			f.contexts = append(f.contexts, name)
		}
	}
	return nil
}

// marks conflicting local service id (svc.ns),
// so only their global forms (svc.ns.context) get forwarded.
// if global ids are conflicting, an error is returned.
func (f *fwd) checkConflicts() error {
	var (
		locals  = make(map[string]*target, len(f.targets)) // local->target
		aliases = make(map[string]bool)                    // aliases (global)
		dups    = make(map[string]bool)                    // dup globals
	)
	for _, t := range f.targets {
		local := t.local()
		if other, ok := locals[local]; ok {
			// local conflict (ok)
			t.conflict = true
			other.conflict = true
			if t.context == other.context {
				// global conflict (not ok)
				dups[t.context] = true
			}
		} else {
			// no conflict (yet)
			locals[local] = t
		}
		for _, a := range t.aliases {
			if aliases[a] {
				// alias conflict (not ok)
				dups[a] = true
			} else {
				aliases[a] = true
			}
		}
	}

	// collect global dups and return an error
	if len(dups) > 0 {
		sb := &strings.Builder{}
		for d := range dups {
			if sb.Len() > 0 {
				sb.WriteString("; ")
			}
			sb.WriteString(d)
		}
		return fmt.Errorf("duplicate service entries: %s", sb.String())
	}

	return nil
}

// fills target ports by querying for all remote service ports.
// if a service cannot be
func (f *fwd) fillPorts() ([]string, error) {
	// context->local->target.
	// separated for concurrent safety.
	lookup := make(map[string]map[string]*target)
	for _, kctx := range f.contexts {
		lookup[kctx] = make(map[string]*target)
	}
	for _, t := range f.targets {
		lookup[t.context][t.local()] = t
	}

	// func per context to populate ports
	do := func(kctx string) func() error {
		return func() error {
			ports, err := f.kubectl.ports(kctx)
			if err != nil {
				return err
			}
			locals := lookup[kctx]
			for _, p := range ports {
				local := localID(p.namespace, p.service)
				if t, ok := locals[local]; ok {
					// number -> name,proto
					t.ports[p.number] = fmt.Sprintf("%s,%s", p.name, p.proto)
				}
			}
			return nil
		}
	}

	// run per context
	eg := errgroup.Group{}
	for _, kctx := range f.contexts {
		eg.Go(do(kctx))
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	// collect unfilled targets
	var missing []string
	var found []*target
	for _, t := range f.targets {
		if len(t.ports) == 0 {
			missing = append(missing, t.global())
		} else {
			found = append(found, t)
		}
	}

	// continue with existing targets only
	f.targets = found

	if len(missing) > 0 {
		return missing, nil
	}

	return nil, nil
}

// spawns an always-on kubectl port-forward that auto-retries until ctx.done
func (f *fwd) child(ctx context.Context, t *target) func() error {
	return func() error {
		ports := make([]string, 0, len(t.ports))
		for num, txt := range t.ports {
			f.log.Infof("forwarding %s:%s (%s)", t.global(), num, txt)
			if !t.conflict {
				f.log.Infof("forwarding %s:%s (%s)", t.local(), num, txt)
			}
			for _, alias := range t.aliases {
				f.log.Infof("forwarding %s:%s (%s)", alias, num, txt)
			}
			ports = append(ports, num)
		}
		for {
			err := f.kubectl.forward(ctx, t.context, t.namespace, t.service, ports, t.addr)
			if err != nil {
				if errors.Is(err, errDone) {
					return nil
				}
				return err
			}
			f.log.Warnf("transient error detected in %s, reconnecting...", t.global())
		}
	}
}
