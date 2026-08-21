package storage

import (
	"time"
)

const (
	defaultEventTTL            = 24 * time.Hour
	defaultMaxEventBytes       = 10 << 20
	defaultMaxOpenConns        = 10
	defaultMaxIdleConns        = 5
	defaultConnMaxLifetime     = 30 * time.Minute
	defaultTransactionAttempts = 5
)

// Options controls database pooling and event retention.
type Options struct {
	// DataDirectory overrides the platform data directory used by an empty DSN.
	DataDirectory string
	// EventTTL controls how long inactive streams are retained.
	EventTTL time.Duration
	// MaxEventBytes bounds retained event payload bytes across all streams.
	MaxEventBytes int64
	// MaxOpenConns bounds PostgreSQL and MySQL pool size.
	MaxOpenConns int
	// MaxIdleConns bounds idle PostgreSQL and MySQL connections.
	MaxIdleConns int
	// ConnMaxLifetime limits connection reuse. The default remains below common
	// MySQL and proxy idle limits.
	ConnMaxLifetime time.Duration
	// Now is used for retention decisions. It defaults to time.Now.
	Now func() time.Time
}

func (o Options) normalized() Options {
	if o.EventTTL <= 0 {
		o.EventTTL = defaultEventTTL
	}
	if o.MaxEventBytes <= 0 {
		o.MaxEventBytes = defaultMaxEventBytes
	}
	if o.MaxOpenConns <= 0 {
		o.MaxOpenConns = defaultMaxOpenConns
	}
	if o.MaxIdleConns < 0 {
		o.MaxIdleConns = 0
	} else if o.MaxIdleConns == 0 {
		o.MaxIdleConns = defaultMaxIdleConns
	}
	if o.MaxIdleConns > o.MaxOpenConns {
		o.MaxIdleConns = o.MaxOpenConns
	}
	if o.ConnMaxLifetime <= 0 {
		o.ConnMaxLifetime = defaultConnMaxLifetime
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}
