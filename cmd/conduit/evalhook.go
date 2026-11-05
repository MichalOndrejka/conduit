package main

import (
	"context"
	"io"

	"github.com/MichalOndrejka/conduit/internal/config"
)

// evalCmd runs `conduit eval ...`. It is nil unless the local-only
// eval_local.go (gitignored, alongside internal/eval) registers it.
var evalCmd func(ctx context.Context, cfg *config.AppConfig, args []string, stdout, stderr io.Writer) error
