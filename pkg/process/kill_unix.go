//go:build unix

package process

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// terminateGroup puts the child in its own process group and kills the whole
// group on cancellation. ab-av1 spawns ffmpeg, and killing only the direct
// child leaves those grandchildren alive holding the inherited output pipes,
// which blocks Wait long after the job was canceled.
func terminateGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
}
