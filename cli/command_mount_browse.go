package cli

import (
	"os"
	"os/exec"
	"runtime"

	"github.com/skratchdot/open-golang/open"
)

var (
	mountBrowser = mountCommand.Flag("browse", "Browse mounted filesystem using the provided method").Default("OS").Enum("NONE", "WEB", "OS")
)

var mountBrowsers = map[string]func(mountPoint string, addr string) error{
	"NONE": nil,
	"WEB":  openInWebBrowser,
	"OS":   openInOSBrowser,
}

func browseMount(mountPoint string, addr string) error {
	b := mountBrowsers[*mountBrowser]
	if b == nil {
		return nil
	}

	return b(mountPoint, addr)
}

func openInWebBrowser(mountPoint string, addr string) error {
	open.Start(addr)
	return nil
}

func openInOSBrowser(mountPoint string, addr string) error {
	if runtime.GOOS == "windows" {
		return netUSE(mountPoint, addr)
	}

	open.Start(addr)
	return nil
}

func netUSE(mountPoint string, addr string) error {
	c := exec.Command("net", "use", mountPoint, addr)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	c.Run()

	open.Start("x:\\")

	// Wait until ctrl-c pressed
	done := make(chan bool)
	onCtrlC(func() {
		if done != nil {
			close(done)
			done = nil
		}
	})
	<-done

	c = exec.Command("net", "use", mountPoint, "/d")
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	c.Run()

	return nil
}