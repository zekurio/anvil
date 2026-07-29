//go:build !unix

package process

import "os/exec"

// terminateGroup has no portable equivalent outside Unix, so cancellation falls
// back to the os/exec default of killing only the direct child.
func terminateGroup(*exec.Cmd) {}
