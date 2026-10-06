package rs

import (
	"log"

	"github.com/bazelbuild/bazel-gazelle/config"
)

// Gazelle's extension callbacks cannot return errors. Honor its standard strict
// mode so callers can distinguish incomplete inference from a successful update.
func diagnostic(c *config.Config, format string, args ...interface{}) {
	if c.Strict {
		log.Fatalf(format, args...)
	}
	log.Printf(format, args...)
}
