//go:build integration

package mysql

import (
	"context"
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
