package config

import (
	"fmt"
	"io"

	"github.com/Carudy/pai/internal/ui"
)

const PAI_VERSION = "v0.2.2"

var SupportedProviders = []string{"deepseek", "mistral"}

type CliFlags struct {
	Version bool
	Debug   bool
	Inter   bool
	Agent   string
	Input   string
}

var AppFlags CliFlags

func DebugLog(outio io.Writer, format string, a ...any) {
	if AppFlags.Debug {
		fmt.Fprintf(outio, ui.Styles["Debug"].Render(format), a...)
	}
}

func ErrorLog(outio io.Writer, format string, a ...any) {
	fmt.Fprintf(outio, ui.Styles["Error"].Render(format), a...)
}
