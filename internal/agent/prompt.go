package agent

import (
	"fmt"
	"os"
	"runtime"
	"time"
)


func get_sys_prompt() string {
	// Inject OS info
	osInfo := fmt.Sprintf("%s %s", runtime.GOOS, runtime.GOARCH)
	// Inject User info
	userInfo := fmt.Sprintf("%s", os.Getenv("USER"))
	// Inject datetime
	now := time.Now()
	dateTime := fmt.Sprintf("%s %s", now.Format("2006-01-02"), now.Format("15:04:05"))
	// Inject working dir
	wd, _ := os.Getwd()
	return fmt.Sprintf("OS: %s User: %s\nDatetime: %s\nWorking Dir: %s\n", osInfo, userInfo, dateTime, wd)
}
