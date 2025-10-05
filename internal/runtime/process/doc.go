// Package process provides a runtime implementation that supervises local processes.
//
// Full process-group termination is only guaranteed on Linux, where the runtime can
// rely on the operating system's job-control semantics to deliver signals to every
// member of the child process group. On macOS and Windows the supervisor offers
// best-effort semantics: signals are delivered to the direct child, but without
// kernel-enforced job control any grandchildren may remain running and must be
// cleaned up separately by the caller.
//
// On Windows, for example, the Stop and Kill routines in stop_windows.go send an
// interrupt and, if necessary, terminate only the top-level process. Ensuring that
// the entire tree exits would require additional tooling such as job objects or
// other host-specific integrations.
package process
