//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func enableTerminalVT() bool {
	h := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	const enableVT = 0x0004
	if mode&enableVT == 0 {
		if err := windows.SetConsoleMode(h, mode|enableVT); err != nil {
			return false
		}
	}
	return true
}
