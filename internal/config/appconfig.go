package config

import (
	"fmt"
	"io"

	"github.com/Carudy/pai/internal/ui"
)

var SupportedProviders = []string{"deepseek", "mistral"}

type CliFlags struct {
	Debug bool
	Inter bool
	Agent string
	Input string
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
