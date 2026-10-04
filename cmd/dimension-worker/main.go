package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/patik/mcui/internal/dimension"
)

type event struct {
	Type       string             `json:"type"`
	Message    string             `json:"message,omitempty"`
	Dimensions []dimension.Result `json:"dimensions,omitempty"`
	Completed  int                `json:"completed,omitempty"`
	Total      int                `json:"total,omitempty"`
}

func emit(e event) { _ = json.NewEncoder(os.Stdout).Encode(e) }
func main() {
	if len(os.Args) < 2 {
		fail("missing worker mode")
	}
	mode := os.Args[1]
	flags := flag.NewFlagSet("dimension-worker", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	source := flags.String("source", "", "source world")
	target := flags.String("target", "", "target world")
	name := flags.String("dimension", "", "dimension name")
	id := flags.Int("dimension-id", 0, "dimension ID")
	allowMissing := flags.Bool("allow-missing", false, "allow retry after partial removal")
	if err := flags.Parse(os.Args[2:]); err != nil {
		fail(err.Error())
	}
	progress := func(message string, done, total int) {
		emit(event{Type: "progress", Message: message, Completed: done, Total: total})
	}
	var result dimension.Result
	var err error
	switch mode {
	case "inspect":
		var list []map[string]any
		list, err = dimension.Inspect(*source)
		if err == nil {
			out := make([]dimension.Result, 0, len(list))
			_ = out
			raw := make([]json.RawMessage, 0, len(list))
			for _, item := range list {
				b, _ := json.Marshal(item)
				raw = append(raw, b)
			}
			_ = json.NewEncoder(os.Stdout).Encode(struct {
				Type       string            `json:"type"`
				Dimensions []json.RawMessage `json:"dimensions"`
			}{"result", raw})
			return
		}
	case "check", "apply":
		result, err = dimension.RunImport(*source, *target, *name, mode == "apply", progress)
	case "remove-check", "remove":
		if *id <= 0 {
			err = fmt.Errorf("invalid dimension ID")
		} else {
			result, err = dimension.Remove(*target, *name, int32(*id), *allowMissing, mode == "remove", progress)
		}
	default:
		err = fmt.Errorf("unsupported worker mode %q", mode)
	}
	if err != nil {
		fail(err.Error())
	}
	emit(event{Type: "result", Dimensions: []dimension.Result{result}})
}
func fail(message string) { emit(event{Type: "error", Message: message}); os.Exit(1) }
