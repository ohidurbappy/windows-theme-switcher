//go:build !windows
// +build !windows

package main

import "fmt"

func main() {
	fmt.Println("This application is designed to run on Windows only.")
	fmt.Println("The Windows-specific system tray implementation requires Windows APIs.")
}
