# `fwd`

`kubectl port-forward` with multiple targets and auto-recovery.  



## `Usage`

`fwd` looks for a local `.fwd.yaml`, then for `$HOME/.fwd.yaml`.  
Here's an example of a multi-context multi-namespace forwarding config.

```yaml
cidr: 127.0.15.1/24 # if omitted, defaults to 127.0.11.0/24
contexts:
- name: prod-cluster # if omitted, defaults to current-context
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
