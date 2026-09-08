//go:build linux

package main

import (
	"os/exec"
	"time"
)

func (a *app) audioEnsure() {
	if a.audioStopped {
		return
	}
	// ALSA 直写需独占设备,先停 PipeWire。
	exec.Command("systemctl", "--user", "stop",
		"pipewire.socket", "pipewire-pulse.socket",
		"pipewire.service", "wireplumber.service", "pipewire-pulse.service").Run()
	time.Sleep(300 * time.Millisecond)
	a.audioStopped = true
}

func (a *app) restoreAudio() {
	if !a.audioStopped {
		return
	}
	exec.Command("systemctl", "--user", "start",
		"pipewire.service", "wireplumber.service", "pipewire-pulse.service",
		"pipewire.socket", "pipewire-pulse.socket").Run()
	a.audioStopped = false
}
