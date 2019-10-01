# `fwd`

`kubectl port-forward` with multiple targets and auto-recovery. 

[![Build Status](https://travis-ci.org/adamglt/fwd.svg?branch=master)](https://travis-ci.org/adamglt/fwd) [![GoDoc](https://godoc.org/github.com/adamglt/fwd?status.svg)](https://godoc.org/github.com/adamglt/fwd)

## `Usage`

`go get -u github.com/adamglt/fwd` or download a binary from the releases tab.

`fwd` looks for a local `.fwd.yaml`, then for `$HOME/.fwd.yaml`.  
Here's an example of a multi-context multi-namespace forwarding config.

```yaml
cidr: 127.0.15.1/24 # local cidr - defaults to 127.0.11.0/24
contexts:
- name: prod-cluster # empty name defaults to $(kubectl config current-context)
  namespaces:
  - name: prod-ns-1
    services:
    - name: svc1
    - name: svc2
- name: minikube
  namespaces:
  - name: ns1
    services:
    - name: svc1
    - name: svc2
  - name: ns2
    services:
    - name: svc1
    - name: svc2

``` 

`fwd` proxies calls to `kubectl port-forward svc/<...>` for every port the service exposes.  
The endpoints are then made available using their local names (`<svc>.<namespace>:<port>`) and global names (`<svc>.<namespace>.<context>:<port>`).  
When the local name exists in more than one context, only the global name is made available.

`fwd` is very similar to the popular https://github.com/txn2/kubefwd in concept, with three major differences:
- it is config-based and not flag-based
- it natively supports multiple contexts (for teams working with more than one cluster)
- it uses a simple but effective per-connection error recovery

## `Requirements`

- `fwd` uses `kubectl` for actual forwarding, so that's required.
- Only Linux and macOS are supported at the moment (PRs welcome). 
- `sudo` is required - the tool has to modify `/etc/hosts` for the local/global names to be resolvable.  
In macOS we also have to create aliases for `lo0` (both are cleaned up on exit).

## License

MIT