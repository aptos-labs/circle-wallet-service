//go:build integration

package mysql

import (
	"context"
	"os"
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
	t.Cleanup(func() { cleanTables(t, s) })
	t.Cleanup(func() { _ = sqlDB.Close() })
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

func TestClaimNextQueued(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	sender := "0xsame"
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
	claimed, err := s.ClaimNextQueued(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil {
		t.Fatal("expected claim")
	}
	if claimed.Status != store.StatusProcessing {
		t.Fatalf("status %s", claimed.Status)
	}
	if claimed.SequenceNumber == nil || *claimed.SequenceNumber != 0 {
		t.Fatalf("seq %+v", claimed.SequenceNumber)
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
		c, err := s.ClaimNextQueued(ctx)
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

func TestReconcileSequence(t *testing.T) {
	s := testDB(t)
	ctx := context.Background()
	sender := "0xrecon"
	if err := s.UpsertNextSequence(ctx, sender, 5); err != nil {
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
		if _, err := s.ClaimNextQueued(ctx); err != nil {
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
