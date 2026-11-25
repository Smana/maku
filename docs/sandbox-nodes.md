<!-- page-type: reference -->

# Sandbox nodes

Every run pod runs under the RuntimeClass `maku.sandbox.runtimeClassName` names, and the
run-pod admission policy refuses any other. The nodes, and the runtime on them, are yours.
`sandbox.unsafeAllowRunc` runs agents on the node's own runtime: the demo and kind only, and the
render refuses it without the demo's `demoValues`.

| Platform | The runtime | `sandbox.runtimeClassName` |
|---|---|---|
| GKE | a node pool with GKE Sandbox enabled, which brings gVisor and its RuntimeClass `gvisor` (handler `gvisor`) | `gvisor` |
| k3s | gVisor's `runsc` and its containerd shim on each node, added to k3s's containerd through its config template | `gvisor` |
| kubeadm | gVisor's `runsc`, or Kata Containers, registered as a containerd runtime handler | `gvisor`, or your Kata class |
| EKS | no managed sandbox: a node group whose image (a custom AMI, or a Bottlerocket variant) carries a `runsc` or Kata handler in containerd | the class you create for it |

Where the cluster has the handler but no RuntimeClass, `maku.runController.createRuntimeClass: true`
with `runtimeHandler` renders one under the class's name. The preflight's `runtimeclass` check fails
`runtimeclass_missing` when the class does not exist, and the run-controller refuses runs until it
does ([operations](operations/failures.md#the-run-controllers-preflight)).

A sandbox pool may scale from zero: the preflight's network probe is often the pod that wakes it,
and it waits up to 10 minutes for a scale-up the cluster autoscaler triggered before it refuses runs
([operations](operations/failures.md#the-run-controllers-preflight)).

A cold sandbox node costs a run 1.5 to 3.5 minutes at start: the image pull and the sandbox's own
boot land on the run's clock, before its clone. A low-priority placeholder pod that keeps the run
images on one node removes most of it for the price of one idle pod. The nodes are yours,
so the chart ships none: schedule the placeholder on a window yourself (for example, on the reference
platform, 08:00-19:00 Europe/Paris on weekdays), and let the mechanism own the `replicas`
field so Flux does not revert it.
