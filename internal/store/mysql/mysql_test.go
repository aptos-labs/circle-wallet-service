//go:build integration

package mysql

import (
	"context"
	"database/sql"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/aptos-labs/jc-contract-integration/internal/db"
	"github.com/aptos-labs/jc-contract-integration/internal/store"
	"github.com/aptos-labs/jc-contract-integration/internal/webhook"
	"github.com/google/uuid"
)

func testDB(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set")
	}
	if err := db.Migrate(dsn); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	s := New(sqlDB)
	// Cleanup order matters: t.Cleanup runs LIFO, so register close LAST so
	// it runs AFTER the per-test cleanTables. Otherwise cleanTables hits a
	// closed DB.
	t.Cleanup(func() { _ = sqlDB.Close() })
	t.Cleanup(func() { cleanTables(t, s) })
	return s
}

func cleanTables(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		"DELETE FROM webhook_deliveries",
		"DELETE FROM transactions",
		"DELETE FROM account_sequences",
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("clean tables: %v", err)
		}
	}
}

func testTxn(sender string, status store.TxnStatus) *store.TransactionRecord {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return &store.TransactionRecord{
		ID:            uuid.New().String(),
		SenderAddress: sender,
		WalletID:      "wallet-1",
		Status:        status,
		FunctionID:    "0x1::mod::entry",
		PayloadJSON:   "{}",
		ExpiresAt:     now.Add(time.Hour),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

func TestCreateAndGet(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	rec := testTxn("0xaaa", store.StatusQueued)
	if err := s.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected record")
	}
	if got.ID != rec.ID || got.SenderAddress != rec.SenderAddress || got.Status != rec.Status ||
		got.WalletID != rec.WalletID || got.FunctionID != rec.FunctionID || got.PayloadJSON != rec.PayloadJSON {
		t.Fatalf("mismatch: %+v vs %+v", got, rec)
	}
}

func TestGetByIdempotencyKey(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	rec := testTxn("0xbbb", store.StatusQueued)
	rec.IdempotencyKey = "idem-" + uuid.New().String()
	if err := s.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetByIdempotencyKey(ctx, rec.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != rec.ID {
		t.Fatalf("got %+v want id %s", got, rec.ID)
	}
}

func TestGetByIdempotencyKeyMiss(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	got, err := s.GetByIdempotencyKey(ctx, "no-such-key-"+uuid.New().String())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestListByStatus(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Millisecond)
	for i, st := range []store.TxnStatus{store.StatusQueued, store.StatusConfirmed, store.StatusFailed} {
		rec := testTxn("0xs"+string(rune('1'+i)), st)
		rec.ID = uuid.New().String()
		rec.CreatedAt = base.Add(time.Duration(i) * time.Second)
		rec.UpdatedAt = rec.CreatedAt
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListByStatus(ctx, store.StatusQueued)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("queued count: got %d want 1", len(list))
	}
	if list[0].Status != store.StatusQueued {
		t.Fatalf("status: %s", list[0].Status)
	}
}

func TestUpdateIfStatus(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	rec := testTxn("0xccc", store.StatusQueued)
	if err := s.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	rec2, err := s.Get(ctx, rec.ID)
	if err != nil || rec2 == nil {
		t.Fatal(err)
	}
	rec2.Status = store.StatusConfirmed
	ok, err := s.UpdateIfStatus(ctx, rec2, store.StatusQueued)
	if err != nil || !ok {
		t.Fatalf("first update: ok=%v err=%v", ok, err)
	}
	rec2.Status = store.StatusFailed
	ok, err = s.UpdateIfStatus(ctx, rec2, store.StatusQueued)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected false")
	}
}

func TestFeePayerColumns(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	rec := testTxn("0xfee", store.StatusQueued)
	rec.FeePayerWalletID = "fp-wallet"
	rec.FeePayerAddress = "0xfeeaddr"
	if err := s.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FeePayerWalletID != "fp-wallet" || got.FeePayerAddress != "0xfeeaddr" {
		t.Fatalf("fee payer: %+v", got)
	}
}

func TestClaimNextQueuedForSender(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	sa, sb := "0xsenderA", "0xsenderB"
	rb := testTxn(sb, store.StatusQueued)
	rb.CreatedAt = time.Now().UTC().Add(-time.Hour)
	rb.UpdatedAt = rb.CreatedAt
	if err := s.Create(ctx, rb); err != nil {
		t.Fatal(err)
	}
	ra := testTxn(sa, store.StatusQueued)
	if err := s.Create(ctx, ra); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimNextQueuedForSender(ctx, sa)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != ra.ID {
		t.Fatalf("got %+v want %s", claimed, ra.ID)
	}
}

func TestListQueuedSenders(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	for _, addr := range []string{"0xq1", "0xq2", "0xq3"} {
		rec := testTxn(addr, store.StatusQueued)
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	addrs, err := s.ListQueuedSenders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(addrs) != 3 {
		t.Fatalf("got %d senders: %v", len(addrs), addrs)
	}
	want := map[string]struct{}{"0xq1": {}, "0xq2": {}, "0xq3": {}}
	for _, a := range addrs {
		delete(want, a)
	}
	if len(want) != 0 {
		t.Fatalf("missing senders: %v", want)
	}
}

func TestAtomicSequenceAllocation(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	sender := "0xseq"
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := range 3 {
		rec := testTxn(sender, store.StatusQueued)
		rec.ID = uuid.New().String()
		rec.CreatedAt = now.Add(time.Duration(i) * time.Millisecond)
		rec.UpdatedAt = rec.CreatedAt
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	var seqs []uint64
	for range 3 {
		c, err := s.ClaimNextQueuedForSender(ctx, sender)
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, *c.SequenceNumber)
	}
	if seqs[0] != 0 || seqs[1] != 1 || seqs[2] != 2 {
		t.Fatalf("sequences %v", seqs)
	}
}

func TestRecoverStaleProcessing(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	rec := testTxn("0xrecover", store.StatusQueued)
	if err := s.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE transactions SET status = ?, sequence_number = 0,
			updated_at = UTC_TIMESTAMP(3) - INTERVAL 120 SECOND WHERE id = ?`,
		string(store.StatusProcessing), rec.ID); err != nil {
		t.Fatal(err)
	}
	n, err := s.RecoverStaleProcessing(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows %d", n)
	}
	got, err := s.Get(ctx, rec.ID)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusQueued || got.SequenceNumber != nil {
		t.Fatalf("after recover: %+v", got)
	}
}

// TestRecoverStaleProcessing_NoSequenceNumber covers the defense-in-depth case
// where a row ends up in processing without a sequence_number — e.g. operator
// intervention, a future claim-path bug, or the E2E TestStaleProcessingRecovery
// injection path. Such rows must be rescued (otherwise they wedge forever), and
// because they never burned a sequence, the per-sender counter must NOT be
// decremented (decrementing would drift the counter below truth).
func TestRecoverStaleProcessing_NoSequenceNumber(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	sender := "0xnoseq"

	// Seed the sender's counter at 5 so we can assert it stays at 5 after
	// recovery (no decrement).
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO account_sequences (sender_address, next_sequence) VALUES (?, ?)`,
		sender, 5); err != nil {
		t.Fatal(err)
	}

	rec := testTxn(sender, store.StatusQueued)
	if err := s.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	// Flip to processing WITHOUT setting sequence_number (mirrors the E2E
	// injection path). updated_at 120s ago so it crosses the threshold.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE transactions SET status = ?,
			updated_at = UTC_TIMESTAMP(3) - INTERVAL 120 SECOND WHERE id = ?`,
		string(store.StatusProcessing), rec.ID); err != nil {
		t.Fatal(err)
	}

	n, err := s.RecoverStaleProcessing(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row recovered, got %d", n)
	}

	got, err := s.Get(ctx, rec.ID)
	if err != nil || got == nil {
		t.Fatalf("get after recover: %+v err=%v", got, err)
	}
	if got.Status != store.StatusQueued || got.SequenceNumber != nil {
		t.Fatalf("row not reset to queued: %+v", got)
	}

	// Counter must be unchanged at 5 — the stranded row never burned a sequence.
	if v := queryNextSeq(t, s, sender); v != 5 {
		t.Fatalf("counter drifted: got %d want 5", v)
	}
}

// TestRecoverStaleProcessing_MixedSequencedAndNot verifies that a single sweep
// correctly handles a batch containing both kinds of stale rows: the counter
// is decremented only by the count of rows that actually had a sequence.
func TestRecoverStaleProcessing_MixedSequencedAndNot(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	sender := "0xmixed"

	// Counter seeded at 10.
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO account_sequences (sender_address, next_sequence) VALUES (?, ?)`,
		sender, 10); err != nil {
		t.Fatal(err)
	}

	// Two rows with sequence; one without.
	seqRows := []struct {
		seq sql.NullInt64
	}{
		{sql.NullInt64{Int64: 7, Valid: true}},
		{sql.NullInt64{Int64: 8, Valid: true}},
		{},
	}
	var ids []string
	for _, r := range seqRows {
		rec := testTxn(sender, store.StatusQueued)
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rec.ID)
		if r.seq.Valid {
			if _, err := s.db.ExecContext(ctx, `
				UPDATE transactions SET status = ?, sequence_number = ?,
					updated_at = UTC_TIMESTAMP(3) - INTERVAL 120 SECOND WHERE id = ?`,
				string(store.StatusProcessing), r.seq.Int64, rec.ID); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := s.db.ExecContext(ctx, `
				UPDATE transactions SET status = ?,
					updated_at = UTC_TIMESTAMP(3) - INTERVAL 120 SECOND WHERE id = ?`,
				string(store.StatusProcessing), rec.ID); err != nil {
				t.Fatal(err)
			}
		}
	}

	n, err := s.RecoverStaleProcessing(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("expected 3 rows recovered, got %d", n)
	}

	// All three rows are now queued with cleared sequence.
	for _, id := range ids {
		got, err := s.Get(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("get %s: %+v err=%v", id, got, err)
		}
		if got.Status != store.StatusQueued || got.SequenceNumber != nil {
			t.Fatalf("row %s not reset: %+v", id, got)
		}
	}

	// Counter decremented by 2 (two sequenced rows) — NOT by 3.
	if v := queryNextSeq(t, s, sender); v != 8 {
		t.Fatalf("counter: got %d want 8 (10 - 2 sequenced)", v)
	}
}

func TestReconcileSequence(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	sender := "0xrecon"
	if _, err := s.db.ExecContext(ctx, `INSERT INTO account_sequences (sender_address, next_sequence) VALUES (?, ?)`, sender, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileSequence(ctx, sender, 10); err != nil {
		t.Fatal(err)
	}
	if v := queryNextSeq(t, s, sender); v != 10 {
		t.Fatalf("after 10: %d", v)
	}
	if err := s.ReconcileSequence(ctx, sender, 3); err != nil {
		t.Fatal(err)
	}
	if v := queryNextSeq(t, s, sender); v != 10 {
		t.Fatalf("after 3: %d", v)
	}
}

func TestForceResetSequenceToChain(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	sender := "0xforcereset"

	if _, err := s.db.ExecContext(ctx, `INSERT INTO account_sequences (sender_address, next_sequence) VALUES (?, ?)`, sender, 200); err != nil {
		t.Fatal(err)
	}

	// No in-flight submitted rows — counter should snap straight to chainSeq.
	if err := s.ForceResetSequenceToChain(ctx, sender, 42); err != nil {
		t.Fatal(err)
	}
	if v := queryNextSeq(t, s, sender); v != 42 {
		t.Fatalf("no-inflight reset: got %d want 42", v)
	}

	// Two in-flight submitted rows past the new chainSeq → counter should
	// be chainSeq + 2 so the next claim doesn't collide with them.
	createSubmittedRow(t, s, sender, 100, 50)
	createSubmittedRow(t, s, sender, 101, 51)
	// And one below the chainSeq threshold that must NOT be counted.
	createSubmittedRow(t, s, sender, 102, 40)

	if err := s.ForceResetSequenceToChain(ctx, sender, 50); err != nil {
		t.Fatal(err)
	}
	if v := queryNextSeq(t, s, sender); v != 52 {
		t.Fatalf("with-inflight reset: got %d want 52 (chain=50 + 2 inflight >=50)", v)
	}
}

func createSubmittedRow(t *testing.T, s *Store, sender string, seedID int, seqNum uint64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	rec := &store.TransactionRecord{
		ID:             uuid.New().String(),
		SenderAddress:  sender,
		WalletID:       "wallet-1",
		Status:         store.StatusSubmitted,
		FunctionID:     "0x1::mod::entry",
		PayloadJSON:    "{}",
		SequenceNumber: &seqNum,
		TxnHash:        "0xdeadbeef",
		ExpiresAt:      now.Add(time.Hour),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.Create(ctx, rec); err != nil {
		t.Fatalf("seed submitted row %d: %v", seedID, err)
	}
	// Create seeds the row as whatever status is on the record, but if your
	// Create enforces "queued" we'd need to flip it. Normalize here.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE transactions SET status = ?, sequence_number = ? WHERE id = ?`,
		string(store.StatusSubmitted), seqNum, rec.ID); err != nil {
		t.Fatalf("flip to submitted: %v", err)
	}
}

func queryNextSeq(t *testing.T, s *Store, sender string) uint64 {
	t.Helper()
	var v uint64
	err := s.db.QueryRowContext(context.Background(),
		`SELECT next_sequence FROM account_sequences WHERE sender_address = ?`, sender).Scan(&v)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestShiftSenderSequences(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	sender := "0xshift"
	now := time.Now().UTC().Truncate(time.Millisecond)
	var ids []string
	for i := range 3 {
		rec := testTxn(sender, store.StatusQueued)
		rec.ID = uuid.New().String()
		ids = append(ids, rec.ID)
		rec.CreatedAt = now.Add(time.Duration(i) * time.Millisecond)
		rec.UpdatedAt = rec.CreatedAt
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		if _, err := s.ClaimNextQueuedForSender(ctx, sender); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ShiftSenderSequences(ctx, sender, 0); err != nil {
		t.Fatal(err)
	}
	r0, _ := s.Get(ctx, ids[0])
	r1, _ := s.Get(ctx, ids[1])
	r2, _ := s.Get(ctx, ids[2])
	if r0.Status != store.StatusProcessing || r0.SequenceNumber == nil || *r0.SequenceNumber != 0 {
		t.Fatalf("r0: %+v", r0)
	}
	if r1.Status != store.StatusQueued || r1.SequenceNumber != nil {
		t.Fatalf("r1: %+v", r1)
	}
	if r2.Status != store.StatusQueued || r2.SequenceNumber != nil {
		t.Fatalf("r2: %+v", r2)
	}
}

func TestCreateDelivery(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	txnID := uuid.New().String()
	rec := testTxn("0xwh", store.StatusQueued)
	rec.ID = txnID
	if err := s.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	d := &webhook.DeliveryRecord{
		ID:            uuid.New().String(),
		TransactionID: txnID,
		URL:           "https://example.com/hook",
		Payload:       `{"x":1}`,
		Status:        "pending",
		Attempts:      0,
		NextRetryAt:   time.Now().UTC().Add(-time.Minute),
		CreatedAt:     time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := s.CreateDelivery(ctx, d); err != nil {
		t.Fatal(err)
	}
}

func TestClaimPendingDeliveries(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	txnID := uuid.New().String()
	rec := testTxn("0xw1", store.StatusQueued)
	rec.ID = txnID
	if err := s.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	for range 3 {
		d := &webhook.DeliveryRecord{
			ID:            uuid.New().String(),
			TransactionID: txnID,
			URL:           "https://example.com/h",
			Payload:       `{}`,
			Status:        "pending",
			NextRetryAt:   past,
			CreatedAt:     time.Now().UTC().Truncate(time.Millisecond),
		}
		if err := s.CreateDelivery(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.ClaimPendingDeliveries(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("len %d", len(out))
	}
	for _, d := range out {
		if d.Status != "delivering" {
			t.Fatalf("status %s", d.Status)
		}
	}
}

func TestClaimPendingDeliveriesRespectsFutureRetry(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	txnID := uuid.New().String()
	rec := testTxn("0xw2", store.StatusQueued)
	rec.ID = txnID
	if err := s.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	d := &webhook.DeliveryRecord{
		ID:            uuid.New().String(),
		TransactionID: txnID,
		URL:           "https://example.com/h",
		Payload:       `{}`,
		Status:        "pending",
		NextRetryAt:   time.Now().UTC().Add(time.Hour),
		CreatedAt:     time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := s.CreateDelivery(ctx, d); err != nil {
		t.Fatal(err)
	}
	out, err := s.ClaimPendingDeliveries(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("claimed %v", out)
	}
}

func TestUpdateDelivery(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	txnID := uuid.New().String()
	rec := testTxn("0xw3", store.StatusQueued)
	rec.ID = txnID
	if err := s.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	d := &webhook.DeliveryRecord{
		ID:            uuid.New().String(),
		TransactionID: txnID,
		URL:           "https://example.com/h",
		Payload:       `{}`,
		Status:        "pending",
		NextRetryAt:   time.Now().UTC().Add(-time.Minute),
		CreatedAt:     time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := s.CreateDelivery(ctx, d); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimPendingDeliveries(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatal(err)
	}
	up := claimed[0]
	up.Status = "delivered"
	up.Attempts = 1
	now := time.Now().UTC()
	up.LastAttemptAt = &now
	if err := s.UpdateDelivery(ctx, up); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListByTransactionID(ctx, txnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Status != "delivered" {
		t.Fatalf("list %+v", list)
	}
}

func TestListByTransactionID(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	txnA := uuid.New().String()
	txnB := uuid.New().String()
	for _, id := range []string{txnA, txnB} {
		rec := testTxn("0xw4", store.StatusQueued)
		rec.ID = id
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().UTC().Add(-time.Minute)
	for range 2 {
		d := &webhook.DeliveryRecord{
			ID:            uuid.New().String(),
			TransactionID: txnA,
			URL:           "https://example.com/h",
			Payload:       `{}`,
			Status:        "pending",
			NextRetryAt:   past,
			CreatedAt:     time.Now().UTC().Truncate(time.Millisecond),
		}
		if err := s.CreateDelivery(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	other := &webhook.DeliveryRecord{
		ID:            uuid.New().String(),
		TransactionID: txnB,
		URL:           "https://example.com/h",
		Payload:       `{}`,
		Status:        "pending",
		NextRetryAt:   past,
		CreatedAt:     time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := s.CreateDelivery(ctx, other); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListByTransactionID(ctx, txnA)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("len %d", len(list))
	}
}

func TestFullTransactionLifecycle(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	rec := testTxn("0xfullflow", store.StatusQueued)
	if err := s.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimNextQueuedForSender(ctx, rec.SenderAddress)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.Status != store.StatusProcessing || claimed.SequenceNumber == nil || *claimed.SequenceNumber != 0 {
		t.Fatalf("after claim: %+v", claimed)
	}
	hash := "0xabc123"
	claimed.Status = store.StatusSubmitted
	claimed.TxnHash = hash
	if err := s.Update(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, rec.ID)
	if err != nil || got == nil || got.Status != store.StatusSubmitted {
		t.Fatalf("after submit: %+v err=%v", got, err)
	}
	got.Status = store.StatusConfirmed
	ok, err := s.UpdateIfStatus(ctx, got, store.StatusSubmitted)
	if err != nil || !ok {
		t.Fatalf("confirm: ok=%v err=%v", ok, err)
	}
	final, err := s.Get(ctx, rec.ID)
	if err != nil || final == nil || final.Status != store.StatusConfirmed || final.TxnHash != hash {
		t.Fatalf("final: %+v err=%v", final, err)
	}
}

func TestConcurrentClaimsDifferentSenders(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	sa, sb := "0xconcurrentA", "0xconcurrentB"
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := range 5 {
		ra := testTxn(sa, store.StatusQueued)
		ra.ID = uuid.New().String()
		ra.CreatedAt = now.Add(time.Duration(i) * time.Millisecond)
		ra.UpdatedAt = ra.CreatedAt
		if err := s.Create(ctx, ra); err != nil {
			t.Fatal(err)
		}
		rb := testTxn(sb, store.StatusQueued)
		rb.ID = uuid.New().String()
		rb.CreatedAt = now.Add(time.Duration(i) * time.Millisecond)
		rb.UpdatedAt = rb.CreatedAt
		if err := s.Create(ctx, rb); err != nil {
			t.Fatal(err)
		}
	}
	var seqA, seqB []uint64
	var seqMu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 5 {
			c, err := s.ClaimNextQueuedForSender(ctx, sa)
			if err != nil {
				t.Error(err)
				return
			}
			if c == nil || c.SequenceNumber == nil {
				t.Error("nil claim A")
				return
			}
			seqMu.Lock()
			seqA = append(seqA, *c.SequenceNumber)
			seqMu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for range 5 {
			c, err := s.ClaimNextQueuedForSender(ctx, sb)
			if err != nil {
				t.Error(err)
				return
			}
			if c == nil || c.SequenceNumber == nil {
				t.Error("nil claim B")
				return
			}
			seqMu.Lock()
			seqB = append(seqB, *c.SequenceNumber)
			seqMu.Unlock()
		}
	}()
	wg.Wait()
	sort.Slice(seqA, func(i, j int) bool { return seqA[i] < seqA[j] })
	sort.Slice(seqB, func(i, j int) bool { return seqB[i] < seqB[j] })
	want := []uint64{0, 1, 2, 3, 4}
	for i := range want {
		if i >= len(seqA) || seqA[i] != want[i] {
			t.Fatalf("sender A sequences: got %v want %v", seqA, want)
		}
		if i >= len(seqB) || seqB[i] != want[i] {
			t.Fatalf("sender B sequences: got %v want %v", seqB, want)
		}
	}
}

func TestConcurrentClaimsSameSender(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	sender := "0xconcurrentSame"
	now := time.Now().UTC().Truncate(time.Millisecond)
	ids := make(map[string]struct{})
	for i := range 5 {
		rec := testTxn(sender, store.StatusQueued)
		rec.ID = uuid.New().String()
		ids[rec.ID] = struct{}{}
		rec.CreatedAt = now.Add(time.Duration(i) * time.Millisecond)
		rec.UpdatedAt = rec.CreatedAt
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var claimed []*store.TransactionRecord
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := s.ClaimNextQueuedForSender(ctx, sender)
			if err != nil {
				t.Error(err)
				return
			}
			if c == nil {
				t.Error("nil claim")
				return
			}
			mu.Lock()
			claimed = append(claimed, c)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(claimed) != 5 {
		t.Fatalf("claimed count %d", len(claimed))
	}
	seenID := make(map[string]struct{})
	var seqs []uint64
	for _, c := range claimed {
		if _, ok := ids[c.ID]; !ok {
			t.Fatalf("unknown id %s", c.ID)
		}
		if _, dup := seenID[c.ID]; dup {
			t.Fatalf("duplicate id %s", c.ID)
		}
		seenID[c.ID] = struct{}{}
		if c.SequenceNumber == nil {
			t.Fatal("nil sequence")
		}
		seqs = append(seqs, *c.SequenceNumber)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i, want := range []uint64{0, 1, 2, 3, 4} {
		if i >= len(seqs) || seqs[i] != want {
			t.Fatalf("sequences: got %v", seqs)
		}
	}
}

func TestWebhookOutboxFullFlow(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	txnID := uuid.New().String()
	rec := testTxn("0xwhfull", store.StatusQueued)
	rec.ID = txnID
	if err := s.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	ds := make([]*webhook.DeliveryRecord, 3)
	for i := range ds {
		d := &webhook.DeliveryRecord{
			ID:            uuid.New().String(),
			TransactionID: txnID,
			URL:           "https://example.com/h",
			Payload:       "{}",
			Status:        "pending",
			NextRetryAt:   past.Add(time.Duration(i) * time.Millisecond),
			CreatedAt:     time.Now().UTC().Truncate(time.Millisecond),
		}
		if err := s.CreateDelivery(ctx, d); err != nil {
			t.Fatal(err)
		}
		ds[i] = d
	}
	d1, d2, d3 := ds[0], ds[1], ds[2]
	claimed, err := s.ClaimPendingDeliveries(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 3 {
		t.Fatalf("first claim len %d", len(claimed))
	}
	byID := make(map[string]*webhook.DeliveryRecord)
	for _, d := range claimed {
		if d.Status != "delivering" {
			t.Fatalf("status %s", d.Status)
		}
		byID[d.ID] = d
	}
	first := byID[d1.ID]
	first.Status = "delivered"
	first.Attempts = 1
	now := time.Now().UTC()
	first.LastAttemptAt = &now
	if err := s.UpdateDelivery(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := byID[d2.ID]
	second.Status = "failed"
	second.Attempts = 1
	second.LastAttemptAt = &now
	if err := s.UpdateDelivery(ctx, second); err != nil {
		t.Fatal(err)
	}
	third := byID[d3.ID]
	third.Status = "pending"
	third.NextRetryAt = time.Now().UTC().Add(time.Hour)
	if err := s.UpdateDelivery(ctx, third); err != nil {
		t.Fatal(err)
	}
	out, err := s.ClaimPendingDeliveries(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("second claim: %v", out)
	}
	list, err := s.ListByTransactionID(ctx, txnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("list len %d", len(list))
	}
	st := make(map[string]string)
	for _, d := range list {
		st[d.ID] = d.Status
	}
	if st[d1.ID] != "delivered" || st[d2.ID] != "failed" || st[d3.ID] != "pending" {
		t.Fatalf("statuses %v", st)
	}
}

func TestShiftAndRequeue(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	sender := "shift"
	now := time.Now().UTC().Truncate(time.Millisecond)
	var ids []string
	for i := range 5 {
		rec := testTxn(sender, store.StatusQueued)
		rec.ID = uuid.New().String()
		ids = append(ids, rec.ID)
		rec.CreatedAt = now.Add(time.Duration(i) * time.Millisecond)
		rec.UpdatedAt = rec.CreatedAt
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	seqByID := make(map[string]uint64)
	for range 5 {
		c, err := s.ClaimNextQueuedForSender(ctx, sender)
		if err != nil {
			t.Fatal(err)
		}
		if c == nil || c.SequenceNumber == nil {
			t.Fatal("claim")
		}
		seqByID[c.ID] = *c.SequenceNumber
	}
	var failedID string
	for id, seq := range seqByID {
		if seq == 2 {
			failedID = id
			break
		}
	}
	if failedID == "" {
		t.Fatal("no seq 2")
	}
	failed, err := s.Get(ctx, failedID)
	if err != nil || failed == nil {
		t.Fatal(err)
	}
	failed.Status = store.StatusFailed
	if err := s.Update(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if err := s.ShiftSenderSequences(ctx, sender, 2); err != nil {
		t.Fatal(err)
	}
	var requeued []string
	for _, id := range ids {
		if id == failedID {
			continue
		}
		got, _ := s.Get(ctx, id)
		if got != nil && got.Status == store.StatusQueued {
			requeued = append(requeued, id)
		}
	}
	if len(requeued) != 2 {
		t.Fatalf("requeued %v", requeued)
	}
	var newSeqs []uint64
	for range 2 {
		c, err := s.ClaimNextQueuedForSender(ctx, sender)
		if err != nil {
			t.Fatal(err)
		}
		if c == nil || c.SequenceNumber == nil {
			t.Fatal("re-claim")
		}
		newSeqs = append(newSeqs, *c.SequenceNumber)
	}
	sort.Slice(newSeqs, func(i, j int) bool { return newSeqs[i] < newSeqs[j] })
	want := []uint64{3, 4}
	for i := range want {
		if i >= len(newSeqs) || newSeqs[i] != want[i] {
			t.Fatalf("after shift re-claim sequences: got %v want %v", newSeqs, want)
		}
	}
}

func TestStaleRecoveryDoesNotAffectRecentProcessing(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	sender := "0xstaleRecent"
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := range 2 {
		rec := testTxn(sender, store.StatusQueued)
		rec.ID = uuid.New().String()
		rec.CreatedAt = now.Add(time.Duration(i) * time.Millisecond)
		rec.UpdatedAt = rec.CreatedAt
		if err := s.Create(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		c, err := s.ClaimNextQueuedForSender(ctx, sender)
		if err != nil || c == nil {
			t.Fatalf("claim: %+v err=%v", c, err)
		}
	}
	n, err := s.RecoverStaleProcessing(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rows affected %d", n)
	}
	list, err := s.ListByStatus(ctx, store.StatusProcessing)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("processing count %d", len(list))
	}
}

// seedTerminal creates a terminal-status row with a specific updated_at, which
// is the column the archive methods and paging cursor key off of. idempKey="" means no key.
func seedTerminal(t *testing.T, s *Store, status store.TxnStatus, updated time.Time, idempKey string) *store.TransactionRecord {
	t.Helper()
	ctx := context.Background()
	rec := testTxn("0xarchv", status)
	rec.CreatedAt = updated
	rec.UpdatedAt = updated
	rec.IdempotencyKey = idempKey
	if err := s.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// TestListByStatusPagedCursor verifies the (updated_at, id) cursor advances
// strictly and returns pages in ascending order. Five rows with staggered
// updated_at and pageSize=2 ⇒ pages of (2, 2, 1).
func TestListByStatusPagedCursor(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	var ids []string
	for i := range 5 {
		rec := seedTerminal(t, s, store.StatusConfirmed, base.Add(time.Duration(i)*time.Second), "")
		ids = append(ids, rec.ID)
	}

	var cursorTime time.Time
	var cursorID string
	var seen []string
	for {
		page, err := s.ListByStatusPaged(ctx, store.StatusConfirmed, 2, cursorTime, cursorID)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			seen = append(seen, r.ID)
		}
		last := page[len(page)-1]
		cursorTime = last.UpdatedAt
		cursorID = last.ID
		if len(page) < 2 {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("want 5 rows over paged scan, got %d: %v", len(seen), seen)
	}
	// Rows were seeded with ascending updated_at ⇒ paged scan must preserve that order.
	for i, id := range ids {
		if seen[i] != id {
			t.Errorf("page order mismatch at %d: got %s want %s", i, seen[i], id)
		}
	}
}

// TestListByStatusPagedStatusFilter verifies other statuses are not leaked
// across the page boundary when the cursor walks the index.
func TestListByStatusPagedStatusFilter(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)

	// Interleave confirmed/failed/queued at successive updated_at so the index
	// would return them mixed if the WHERE status filter regressed.
	for i, st := range []store.TxnStatus{
		store.StatusConfirmed, store.StatusFailed, store.StatusConfirmed,
		store.StatusQueued, store.StatusConfirmed,
	} {
		seedTerminal(t, s, st, base.Add(time.Duration(i)*time.Second), "")
	}
	page, err := s.ListByStatusPaged(ctx, store.StatusConfirmed, 10, time.Time{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 3 {
		t.Fatalf("confirmed rows returned=%d want 3", len(page))
	}
	for _, r := range page {
		if r.Status != store.StatusConfirmed {
			t.Errorf("leaked status %s", r.Status)
		}
	}
}

// TestPurgeTerminalOlderThan_FiltersAndRespectsLimit covers three things:
//   - Status filter: only confirmed/failed/expired rows get deleted; queued
//     and submitted must survive even if they're "old".
//   - Cutoff filter: rows newer than cutoff must survive.
//   - Limit: one call deletes at most `limit` rows; remainder is cleaned up on
//     a subsequent call, as the Archiver loop would.
func TestPurgeTerminalOlderThan_FiltersAndRespectsLimit(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-48 * time.Hour)
	fresh := now.Add(-time.Minute)

	// Five aged terminal rows (eligible for purge), plus immune rows:
	for range 5 {
		seedTerminal(t, s, store.StatusConfirmed, old, "")
	}
	keepBecauseFresh := seedTerminal(t, s, store.StatusConfirmed, fresh, "")
	keepBecauseNonTerminal := seedTerminal(t, s, store.StatusQueued, old, "")
	keepBecauseSubmitted := seedTerminal(t, s, store.StatusSubmitted, old, "")

	// First purge with limit=2: deletes exactly 2.
	n, err := s.PurgeTerminalOlderThan(ctx, now.Add(-time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("first purge deleted %d, want 2", n)
	}
	// Second purge: up to 10 more, but only 3 aged terminal rows remain.
	n, err = s.PurgeTerminalOlderThan(ctx, now.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("second purge deleted %d, want 3", n)
	}
	// Third purge: nothing left to delete.
	n, err = s.PurgeTerminalOlderThan(ctx, now.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("third purge deleted %d, want 0", n)
	}
	// Immune rows must still exist.
	for _, id := range []string{keepBecauseFresh.ID, keepBecauseNonTerminal.ID, keepBecauseSubmitted.ID} {
		got, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Errorf("row %s should have been spared by purge", id)
		}
	}
}

// TestPurgeTerminalOlderThan_CascadesWebhookDeliveries verifies the
// ON DELETE CASCADE FK — deleting a transaction removes its outbox rows too.
func TestPurgeTerminalOlderThan_CascadesWebhookDeliveries(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-48 * time.Hour)

	txn := seedTerminal(t, s, store.StatusConfirmed, old, "")
	d := &webhook.DeliveryRecord{
		ID: uuid.New().String(), TransactionID: txn.ID, URL: "https://x",
		Payload: "{}", Status: "pending", Attempts: 0,
		NextRetryAt: old, CreatedAt: old,
	}
	if err := s.CreateDelivery(ctx, d); err != nil {
		t.Fatal(err)
	}
	// Confirm delivery exists before purge.
	pre, err := s.ListByTransactionID(ctx, txn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pre) != 1 {
		t.Fatalf("pre-purge deliveries=%d want 1", len(pre))
	}

	if _, err := s.PurgeTerminalOlderThan(ctx, now.Add(-time.Hour), 10); err != nil {
		t.Fatal(err)
	}
	// Transaction is gone.
	if got, _ := s.Get(ctx, txn.ID); got != nil {
		t.Errorf("txn should have been purged; still present")
	}
	// Webhook rows cascaded.
	post, err := s.ListByTransactionID(ctx, txn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(post) != 0 {
		t.Errorf("webhook rows should have cascaded, got %d", len(post))
	}
}

// TestClearIdempotencyOlderThan_NullsAndReleasesUnique verifies the two
// observable effects: (1) idempotency_key becomes NULL on eligible rows so
// GetByIdempotencyKey no longer finds them, and (2) the UNIQUE slot is
// freed — a new row can Create with the same key without conflict.
// Non-eligible rows (fresh, non-terminal, already-null) are untouched.
func TestClearIdempotencyOlderThan_NullsAndReleasesUnique(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-48 * time.Hour)
	fresh := now.Add(-time.Minute)

	agedKey := "aged-" + uuid.New().String()
	freshKey := "fresh-" + uuid.New().String()
	nonTermKey := "queued-" + uuid.New().String()

	aged := seedTerminal(t, s, store.StatusConfirmed, old, agedKey)
	freshRec := seedTerminal(t, s, store.StatusConfirmed, fresh, freshKey)
	nonTerm := seedTerminal(t, s, store.StatusQueued, old, nonTermKey)

	n, err := s.ClearIdempotencyOlderThan(ctx, now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("cleared %d rows, want exactly 1 (the aged terminal row)", n)
	}

	// Aged row's key must be NULL-equivalent ⇒ GetByIdempotencyKey returns nil.
	if got, _ := s.GetByIdempotencyKey(ctx, agedKey); got != nil {
		t.Errorf("aged key should be cleared; lookup returned %s", got.ID)
	}
	// But the audit row itself still exists.
	if got, _ := s.Get(ctx, aged.ID); got == nil {
		t.Errorf("aged row should still exist (only the key is NULLed)")
	}
	// Fresh and non-terminal keys untouched.
	if got, _ := s.GetByIdempotencyKey(ctx, freshKey); got == nil || got.ID != freshRec.ID {
		t.Errorf("fresh key should remain; got %v", got)
	}
	if got, _ := s.GetByIdempotencyKey(ctx, nonTermKey); got == nil || got.ID != nonTerm.ID {
		t.Errorf("non-terminal key should remain; got %v", got)
	}

	// UNIQUE(idempotency_key) slot is freed: creating a new row with agedKey must succeed.
	reuse := testTxn("0xreuse", store.StatusQueued)
	reuse.IdempotencyKey = agedKey
	if err := s.Create(ctx, reuse); err != nil {
		t.Fatalf("expected reuse of cleared key to succeed, got %v", err)
	}

	// Second run deletes nothing (the aged row's key is already NULL, which
	// the WHERE idempotency_key IS NOT NULL clause filters out).
	n, err = s.ClearIdempotencyOlderThan(ctx, now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("second clear affected %d rows, want 0", n)
	}
}

// TestClearIdempotencyOlderThan_RespectsLimit mirrors the purge-limit test:
// per-call cap, remainder cleaned on subsequent call.
func TestClearIdempotencyOlderThan_RespectsLimit(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-48 * time.Hour)

	for range 4 {
		seedTerminal(t, s, store.StatusConfirmed, old, uuid.New().String())
	}
	n, err := s.ClearIdempotencyOlderThan(ctx, now.Add(-time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("first clear affected %d, want 2", n)
	}
	n, err = s.ClearIdempotencyOlderThan(ctx, now.Add(-time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("second clear affected %d, want 2", n)
	}
}
