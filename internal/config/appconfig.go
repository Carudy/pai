package config

import (
	"fmt"
	"io"
	"strings"

	"github.com/Carudy/pai/internal/ui"
)

const PAI_VERSION = "v0.2.8"

var SupportedProviders = []string{"deepseek", "mistral", "openai"}

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
		msg := fmt.Sprintf(format, a...)
		// Trim trailing newline before splitting to avoid a spurious empty last line
		for _, line := range strings.Split(strings.TrimRight(msg, "\n"), "\n") {
			if line != "" {
				// Style each line individually to avoid Lip Gloss block-width padding
				fmt.Fprintln(outio, ui.Styles["Debug"].Render("[DEBUG] "+line))
			}
		}
	}
}

func ErrorLog(outio io.Writer, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	fmt.Fprint(outio, ui.Styles["Error"].Render(msg))
}
