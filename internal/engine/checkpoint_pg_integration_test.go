package engine

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	infopg "github.com/c1ayooo/xalgorix/v4/internal/infoitem/postgres"
)

func TestPGCheckpointStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv("XALGORIX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("XALGORIX_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := infopg.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewPGCheckpointStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := "engine-test-" + time.Now().Format("20060102T150405.000000000")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM engine_checkpoints WHERE checkpoint_id = $1`, id)
	})
	if _, ok, err := store.Get(ctx, id); err != nil || ok {
		t.Fatalf("missing checkpoint = ok=%v err=%v", ok, err)
	}
	if err := store.Set(ctx, id, []byte("payload-1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, id, []byte("payload-2")); err != nil {
		t.Fatal(err)
	}
	payload, ok, err := store.Get(ctx, id)
	if err != nil || !ok || string(payload) != "payload-2" {
		t.Fatalf("round-trip = %q, %v, %v", payload, ok, err)
	}
}
