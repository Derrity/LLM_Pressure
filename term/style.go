package term

import "os"

const (
	reset  = "\x1b[0m"
	bold   = "1"
	dim    = "2"
	red    = "31"
	green  = "32"
	yellow = "33"
	blue   = "34"
	cyan   = "36"
	gray   = "90"
)

func Enabled() bool {
	return os.Getenv("NO_COLOR") == ""
}

func IsTerminal() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func Style(s string, codes ...string) string {
	if !Enabled() || s == "" {
		return s
	}
	out := "\x1b["
	for i, code := range codes {
		if i > 0 {
			out += ";"
		}
		out += code
	}
	return out + "m" + s + reset
}

func Bold(s string) string   { return Style(s, bold) }
func Dim(s string) string    { return Style(s, dim) }
func Red(s string) string    { return Style(s, red) }
func Green(s string) string  { return Style(s, green) }
func Yellow(s string) string { return Style(s, yellow) }
func Blue(s string) string   { return Style(s, blue) }
func Cyan(s string) string   { return Style(s, cyan) }
func Gray(s string) string   { return Style(s, gray) }
