package hq

import (
	"fmt"
	"io"

	"github.com/Carudy/pai/internal/ui"
)

const PAI_VERSION = "v0.2.9"

type CliFlags struct {
	Version bool
	Debug   bool
	Inter   bool
	Agent   string
	Input   string
}

var Flags CliFlags

func DebugLog(outio io.Writer, format string, a ...any) {
	if Flags.Debug {
		msg := ui.RenderStr("Debug", "[DEBUG] "+fmt.Sprintf(format, a...))
		fmt.Fprintln(outio, msg)
	}
}

func ErrorLog(outio io.Writer, format string, a ...any) {
	msg := ui.RenderStr("Error", "[Error] "+fmt.Sprintf(format, a...))
	fmt.Fprintln(outio, msg)
}
