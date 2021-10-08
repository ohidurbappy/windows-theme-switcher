go-winres simply --icon icon.png --manifest gui
# go build -ldflags="-H=windowsgui" -o "Windows Theme Switcher.exe"
go build -ldflags="-s -w -H=windowsgui" -o "Windows Theme Switcher.exe"