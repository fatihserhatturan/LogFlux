package collector

import (
	"context"
	"github.com/fatihserhatturan/logflux/pkg/models"
)

// Source represents a log source that can stream log entries
type Source interface {
	// Start begins streaming logs to the output channel
	Start(ctx context.Context, out chan<- *models.LogEntry) error

	// Stop gracefully stops the source
	Stop() error

	// Name returns the source identifier
	Name() string
}
