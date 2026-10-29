package sqlite

import (
	"context"
	"database/sql/driver"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// statementCounter counts the SQL statements the read and analytics pools
// execute, so tests can require a statement count independent of data size,
// and keeps their text so tests can require that a table is not read.
type statementCounter struct {
	n       atomic.Int64
	mu      sync.Mutex
	queries []string
}

func (c *statementCounter) count() int64 { return c.n.Load() }

func (c *statementCounter) record(query string) {
	c.n.Add(1)
	c.mu.Lock()
	c.queries = append(c.queries, query)
	c.mu.Unlock()
}

// since returns the statements executed after the first n.
func (c *statementCounter) since(n int64) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.queries[n:]...)
}

type countingConnector struct {
	driver.Connector
	counter *statementCounter
}

func (c countingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn, counter: c.counter}, nil
}

// countingConn delegates every optional interface the SQLite connection
// implements and counts queries, executions and prepared statements.
type countingConn struct {
	driver.Conn
	counter *statementCounter
}

func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.counter.record(query)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.counter.record(query)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	c.counter.record(query)
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}

func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *countingConn) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}

func (c *countingConn) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}

func (c *countingConn) IsValid() bool {
	return c.Conn.(driver.Validator).IsValid()
}

// countingStore opens a store whose read pools count statements.
func countingStore(t testing.TB) (*Store, *statementCounter) {
	t.Helper()
	counter := &statementCounter{}
	s, err := OpenWithOptions(filepath.Join(t.TempDir(), "counted.sqlite3"), Options{wrapConnector: func(c driver.Connector) driver.Connector {
		return countingConnector{Connector: c, counter: counter}
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, counter
}
