# Pod Lifecycle Notes

A Pod begins in the Pending phase while its scheduler decides
which node should run it. Once the kubelet pulls the requested
container images and successfully starts every init container,
the phase transitions to Running. Liveness and readiness probes
then dictate whether traffic is routed to the workload.

Termination is a two-step dance: SIGTERM is delivered, the
preStop hook fires, and after the grace period elapses the
runtime sends SIGKILL. Understanding this state machine is
essential before tuning rolling updates or graceful shutdown.
