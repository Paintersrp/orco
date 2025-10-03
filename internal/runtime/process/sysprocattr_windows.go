//go:build windows

package process

import "os/exec"

func configureCmdSysProcAttr(cmd *exec.Cmd) {}
