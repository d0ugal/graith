package daemonservice

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testReceiptStore(t *testing.T) ReceiptStore {
	t.Helper()
	return ReceiptStore{Root: filepath.Join(t.TempDir(), "service-control"), UID: os.Getuid()}
}

func TestReceiptStorePrimaryAndBackupRecovery(t *testing.T) {
	t.Parallel()

	store := testReceiptStore(t)

	first, err := store.Update(true, func(receipt *Receipt) error {
		receipt.Generations["braw"] = Generation{ID: "braw", Version: "1.0.0"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if first.Transaction != 1 {
		t.Fatalf("first transaction = %d, want 1", first.Transaction)
	}

	if _, err := store.Update(false, func(receipt *Receipt) error {
		receipt.Generations["canny"] = Generation{ID: "canny", Version: "2.0.0"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(store.primaryPath(), []byte("dreich"), 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := store.Load()
	if err != nil {
		t.Fatalf("Load() backup recovery = %v", err)
	}

	if _, ok := recovered.Generations["braw"]; !ok {
		t.Fatalf("backup missing first generation: %#v", recovered.Generations)
	}

	if _, ok := recovered.Generations["canny"]; ok {
		t.Fatalf("backup unexpectedly contains newest generation: %#v", recovered.Generations)
	}

	primary, err := os.ReadFile(store.primaryPath())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := decodeReceipt(primary); err != nil {
		t.Fatalf("backup recovery did not restore primary: %v", err)
	}

	if err := os.WriteFile(store.primaryPath(), []byte("dreich-again"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(store.backupPath(), []byte("thrawn"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Load(); err == nil {
		t.Fatal("Load() accepted corrupt primary and backup")
	}
}

func TestReceiptStoreMissingRequiresExplicitInitialization(t *testing.T) {
	t.Parallel()

	store := testReceiptStore(t)
	if _, err := store.Load(); !errors.Is(err, ErrReceiptMissing) {
		t.Fatalf("Load() = %v, want ErrReceiptMissing", err)
	}

	if _, err := os.Lstat(store.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load() created missing receipt root: %v", err)
	}

	if _, err := store.Update(false, func(*Receipt) error { return nil }); !errors.Is(err, ErrReceiptMissing) {
		t.Fatalf("Update(false) = %v, want ErrReceiptMissing", err)
	}

	if _, err := os.Lstat(store.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Update(false) created missing receipt root: %v", err)
	}
}

func TestReceiptStoreMissingAncestorsAreAnUninitializedStore(t *testing.T) {
	t.Parallel()

	store := ReceiptStore{
		Root: filepath.Join(t.TempDir(), "croft", "service-control"),
		UID:  os.Getuid(),
	}

	if _, err := store.Load(); !errors.Is(err, ErrReceiptMissing) {
		t.Fatalf("Load() = %v, want ErrReceiptMissing", err)
	}

	if _, err := store.Update(false, func(*Receipt) error { return nil }); !errors.Is(err, ErrReceiptMissing) {
		t.Fatalf("Update(false) = %v, want ErrReceiptMissing", err)
	}

	if _, err := os.Lstat(filepath.Dir(store.Root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only receipt operations created missing ancestors: %v", err)
	}
}

func TestProfileAllocatorCapacityPendingAndRelease(t *testing.T) {
	t.Parallel()

	receipt := NewReceipt()

	for index := range ServiceManifest().ProfileSlots {
		profile := "croft-" + Definitions()[index+1].Slot

		lease, created, err := ReserveProfile(&receipt, profile, 501, "braw")
		if err != nil || !created {
			t.Fatalf("ReserveProfile(%s) = (%#v, %v, %v)", profile, lease, created, err)
		}

		if err := CommitProfile(&receipt, lease); err != nil {
			t.Fatal(err)
		}
	}

	if _, _, err := ReserveProfile(&receipt, "overflow", 501, "braw"); !errors.Is(err, ErrSlotCapacity) {
		t.Fatalf("capacity error = %v, want ErrSlotCapacity", err)
	}

	if err := ReleaseProfile(&receipt, "croft-00", false); err == nil {
		t.Fatal("lease released without confirmed unregister")
	}

	if err := ReleaseProfile(&receipt, "croft-00", true); err != nil {
		t.Fatal(err)
	}

	lease, created, err := ReserveProfile(&receipt, "bothy", 501, "canny")
	if err != nil || !created || lease.Slot != "00" {
		t.Fatalf("reused allocation = (%#v, %v, %v), want slot 00", lease, created, err)
	}
}

func TestProfileAllocatorNeverBorrowsAnotherPendingRegistration(t *testing.T) {
	t.Parallel()

	receipt := NewReceipt()
	receipt.Pending = &PendingOperation{
		Kind: "register", Profile: "canny", Slot: "00", UID: 501,
		Generation: "1-braw", Token: "first-owner",
	}

	if _, _, err := ReserveProfile(&receipt, "canny", 501, "2-dreich"); !errors.Is(err, ErrPendingInProgress) {
		t.Fatalf("ReserveProfile() = %v, want pending-owner isolation", err)
	}

	if receipt.Pending.Generation != "1-braw" || receipt.Pending.Token != "first-owner" {
		t.Fatalf("second reservation changed first transaction: %#v", receipt.Pending)
	}
}

func TestReceiptStoreConcurrentAllocationIsBijective(t *testing.T) {
	t.Parallel()

	store := testReceiptStore(t)
	if _, err := store.Update(true, func(*Receipt) error { return nil }); err != nil {
		t.Fatal(err)
	}

	profiles := []string{"braw", "canny", "dreich", "bothy", "strath", "bairn"}

	var wg sync.WaitGroup

	errs := make(chan error, len(profiles))
	for _, profile := range profiles {
		wg.Add(1)
		go func() {
			defer wg.Done()

			_, err := store.Update(false, func(receipt *Receipt) error {
				lease, _, err := ReserveProfile(receipt, profile, os.Getuid(), "braw")
				if err != nil {
					return err
				}

				return CommitProfile(receipt, lease)
			})
			errs <- err
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	receipt, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}

	if len(receipt.Leases) != len(profiles) {
		t.Fatalf("leases = %d, want %d", len(receipt.Leases), len(profiles))
	}

	seen := make(map[string]bool)
	for _, lease := range receipt.Leases {
		if seen[lease.Slot] {
			t.Fatalf("slot %s allocated twice", lease.Slot)
		}

		seen[lease.Slot] = true
	}
}

func TestReceiptStoreRejectsInsecureRoot(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "service-control")
	if err := os.Mkdir(root, 0o777); err != nil { // #nosec G301 -- intentionally unsafe negative fixture.
		t.Fatal(err)
	}

	if err := os.Chmod(root, 0o777); err != nil { // #nosec G302 -- intentionally unsafe negative fixture.
		t.Fatal(err)
	}

	store := ReceiptStore{Root: root, UID: os.Getuid()}
	if _, err := store.Update(true, func(*Receipt) error { return nil }); err == nil {
		t.Fatal("insecure receipt root accepted")
	}
}

func TestDecodeReceiptRejectsChecksumValidDuplicateSlotLease(t *testing.T) {
	receipt := NewReceipt()
	receipt.Leases["braw"] = Lease{Profile: "braw", Slot: "05", UID: os.Getuid()}
	receipt.Leases["canny"] = Lease{Profile: "canny", Slot: "05", UID: os.Getuid()}

	checked, err := receipt.withChecksum()
	if err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(checked)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := decodeReceipt(data); err == nil || !strings.Contains(err.Error(), "assigns slot") {
		t.Fatalf("decodeReceipt() = %v, want duplicate-slot rejection", err)
	}
}
