package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func GetUserTextInput(prompt string) (string, error) {
	// Print the prompt using the same Warn style as before.
	fmt.Fprintln(os.Stdout, Styles["Warn"].Render(prompt))
	fmt.Fprint(os.Stdout, Styles["Help"].Render("▊ ")+Styles["Hint"].Render("(type/paste here, enter to confirm, ctrl+c to cancel)")+"\n> ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		if scanner.Err() != nil {
			return "", scanner.Err()
		}
		return "", nil
	}

	line := scanner.Text()
	return strings.TrimSpace(line), nil
}
