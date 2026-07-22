package daemonservice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/d0ugal/graith/internal/config"
	"golang.org/x/sys/unix"
)

const receiptSchema = 1

var (
	ErrReceiptMissing = errors.New("daemon service receipt and backup are missing")
	ErrSlotCapacity   = errors.New("all daemon service profile slots are leased")
)

type Generation struct {
	ID          string `json:"id"`
	AppPath     string `json:"app_path"`
	Version     string `json:"version"`
	BundleBuild string `json:"bundle_build"`
	Commit      string `json:"commit"`
	PayloadHash string `json:"payload_hash"`
	TeamID      string `json:"team_id,omitempty"`
	Requirement string `json:"requirement,omitempty"`
}

type Registration struct {
	Slot                 string       `json:"slot"`
	Label                string       `json:"label"`
	RegisteredGeneration string       `json:"registered_generation,omitempty"`
	RunningGeneration    string       `json:"running_generation,omitempty"`
	RunningPID           int          `json:"running_pid,omitempty"`
	Paths                config.Paths `json:"paths,omitempty"`
}

type Lease struct {
	Profile              string       `json:"profile"`
	Slot                 string       `json:"slot"`
	UID                  int          `json:"uid"`
	RegisteredGeneration string       `json:"registered_generation,omitempty"`
	RunningGeneration    string       `json:"running_generation,omitempty"`
	RunningPID           int          `json:"running_pid,omitempty"`
	Paths                config.Paths `json:"paths,omitempty"`
}

type PendingOperation struct {
	Kind              string    `json:"kind"`
	Profile           string    `json:"profile,omitempty"`
	Slot              string    `json:"slot"`
	UID               int       `json:"uid"`
	Generation        string    `json:"generation"`
	Token             string    `json:"token,omitempty"`
	CreatedAt         time.Time `json:"created_at,omitempty"`
	OwnerPID          int       `json:"owner_pid,omitempty"`
	OwnerPIDStartTime int64     `json:"owner_pid_start_time,omitempty"`
}

type StartIntent struct {
	Label      string    `json:"label"`
	Slot       string    `json:"slot"`
	Profile    string    `json:"profile,omitempty"`
	Generation string    `json:"generation"`
	Nonce      string    `json:"nonce"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
}

type Receipt struct {
	Schema      int                    `json:"schema"`
	Transaction uint64                 `json:"transaction"`
	Default     *Registration          `json:"default,omitempty"`
	Leases      map[string]Lease       `json:"leases"`
	Quarantined map[string]string      `json:"quarantined,omitempty"`
	Generations map[string]Generation  `json:"generations,omitempty"`
	Pending     *PendingOperation      `json:"pending,omitempty"`
	Starts      map[string]StartIntent `json:"starts,omitempty"`
	Checksum    string                 `json:"checksum"`
}

func NewReceipt() Receipt {
	return Receipt{
		Schema:      receiptSchema,
		Leases:      make(map[string]Lease),
		Quarantined: make(map[string]string),
		Generations: make(map[string]Generation),
		Starts:      make(map[string]StartIntent),
	}
}

func (r *Receipt) normalize() {
	if r.Leases == nil {
		r.Leases = make(map[string]Lease)
	}

	if r.Quarantined == nil {
		r.Quarantined = make(map[string]string)
	}

	if r.Generations == nil {
		r.Generations = make(map[string]Generation)
	}

	if r.Starts == nil {
		r.Starts = make(map[string]StartIntent)
	}
}

func (r *Receipt) validateStructure() error {
	usedSlots := make(map[string]string, len(r.Leases))

	if r.Default != nil {
		definition := Definitions()[0]
		if r.Default.Slot != definition.Slot || r.Default.Label != definition.Label {
			return errors.New("daemon service receipt has an invalid default registration")
		}
	}

	for profile, lease := range r.Leases {
		definition, err := DefinitionForSlot(lease.Slot)
		if err != nil || definition.Slot == DefaultSlot || lease.Profile != profile {
			return fmt.Errorf("daemon service receipt has an invalid lease for profile %q", profile)
		}

		if err := ProfileForDefinition(definition, profile); err != nil {
			return err
		}

		if existing := usedSlots[lease.Slot]; existing != "" {
			return fmt.Errorf("daemon service receipt assigns slot %s to profiles %q and %q", lease.Slot, existing, profile)
		}

		usedSlots[lease.Slot] = profile
	}

	for slot := range r.Quarantined {
		if _, err := DefinitionForSlot(slot); err != nil {
			return fmt.Errorf("daemon service receipt quarantines invalid slot %q", slot)
		}
	}

	if r.Pending != nil {
		definition, err := DefinitionForSlot(r.Pending.Slot)
		if err != nil {
			return fmt.Errorf("daemon service receipt has an invalid pending slot: %w", err)
		}

		if err := ProfileForDefinition(definition, r.Pending.Profile); err != nil {
			return err
		}

		if r.Pending.Generation == "" {
			return errors.New("daemon service receipt pending operation has no generation")
		}
	}

	for label, intent := range r.Starts {
		definition, err := ValidateMarker(intent.Label, intent.Slot)
		if err != nil || label != intent.Label {
			return fmt.Errorf("daemon service receipt has an invalid start intent for %q", label)
		}

		if err := ProfileForDefinition(definition, intent.Profile); err != nil {
			return err
		}

		if intent.Generation == "" || !validStartupNonce(intent.Nonce) || intent.CreatedAt.IsZero() || !intent.ExpiresAt.After(intent.CreatedAt) {
			return fmt.Errorf("daemon service receipt start intent %q is incomplete", label)
		}
	}

	for id, generation := range r.Generations {
		if id == "" || generation.ID != id {
			return fmt.Errorf("daemon service receipt has an invalid generation entry %q", id)
		}
	}

	return nil
}

func BeginStart(receipt *Receipt, intent StartIntent) error {
	definition, err := ValidateMarker(intent.Label, intent.Slot)
	if err != nil {
		return err
	}

	if err := ProfileForDefinition(definition, intent.Profile); err != nil {
		return err
	}

	if intent.Generation == "" || !validStartupNonce(intent.Nonce) || intent.CreatedAt.IsZero() || !intent.ExpiresAt.After(intent.CreatedAt) {
		return errors.New("daemon service start generation and nonce are required")
	}

	receipt.normalize()

	if existing, ok := receipt.Starts[intent.Label]; ok && existing != intent {
		return fmt.Errorf("daemon service start already pending for %s", intent.Label)
	}

	receipt.Starts[intent.Label] = intent

	return nil
}

func CompleteStart(receipt *Receipt, label, nonce string) error {
	intent, ok := receipt.Starts[label]
	if !ok {
		return fmt.Errorf("no pending daemon service start for %s", label)
	}

	if intent.Nonce != nonce {
		return errors.New("daemon service start nonce mismatch")
	}

	delete(receipt.Starts, label)

	return nil
}

func CancelStart(receipt *Receipt, label, nonce string) error {
	return CompleteStart(receipt, label, nonce)
}

func (r *Receipt) withChecksum() (Receipt, error) {
	checked := *r
	checked.normalize()
	checked.Checksum = ""

	data, err := json.Marshal(checked)
	if err != nil {
		return Receipt{}, err
	}

	sum := sha256.Sum256(data)
	checked.Checksum = hex.EncodeToString(sum[:])

	return checked, nil
}

func decodeReceipt(data []byte) (Receipt, error) {
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return Receipt{}, err
	}

	if receipt.Schema != receiptSchema {
		return Receipt{}, fmt.Errorf("unsupported daemon service receipt schema %d", receipt.Schema)
	}

	if receipt.Checksum == "" {
		return Receipt{}, errors.New("daemon service receipt has no checksum")
	}

	want := receipt.Checksum

	checked, err := receipt.withChecksum()
	if err != nil {
		return Receipt{}, err
	}

	if checked.Checksum != want {
		return Receipt{}, errors.New("daemon service receipt checksum mismatch")
	}

	receipt.normalize()

	if err := receipt.validateStructure(); err != nil {
		return Receipt{}, err
	}

	return receipt, nil
}

// ReceiptStore serializes global profile-slot mutations across every profile.
// Its root is outside profile data_dir so named daemons cannot allocate the
// same static service slot independently.
type ReceiptStore struct {
	Root string
	UID  int
}

func (s ReceiptStore) primaryPath() string { return filepath.Join(s.Root, "receipt.json") }
func (s ReceiptStore) backupPath() string  { return filepath.Join(s.Root, "receipt.previous.json") }
func (s ReceiptStore) lockPath() string    { return filepath.Join(s.Root, "receipt.lock") }

func (s ReceiptStore) ensureRoot(initialize bool) error {
	if s.UID < 0 {
		return fmt.Errorf("invalid receipt owner UID %d", s.UID)
	}

	parent := filepath.Dir(s.Root)

	base := filepath.Base(s.Root)
	if base == "." || base == string(filepath.Separator) {
		return errors.New("invalid daemon service receipt root")
	}

	if err := validateSecureAncestors(parent, s.UID); err != nil {
		if !initialize && errors.Is(err, os.ErrNotExist) {
			return ErrReceiptMissing
		}

		return fmt.Errorf("validate daemon service receipt parent: %w", err)
	}

	parentFD, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}

	defer func() { _ = unix.Close(parentFD) }()

	if initialize {
		if err := unix.Mkdirat(parentFD, base, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("create daemon service receipt root: %w", err)
		}
	}

	rootFD, err := unix.Openat(parentFD, base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if !initialize && errors.Is(err, unix.ENOENT) {
			return ErrReceiptMissing
		}

		return err
	}

	defer func() { _ = unix.Close(rootFD) }()

	if err := validateOwnedDirectoryFD(rootFD, s.UID); err != nil {
		return fmt.Errorf("validate daemon service receipt root: %w", err)
	}

	return nil
}

func secureOwnedPath(path string, uid int, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("symlink is not allowed")
	}

	if directory != info.IsDir() {
		return errors.New("unexpected file type")
	}

	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("permissions %04o are not owner-only", info.Mode().Perm())
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid {
		return fmt.Errorf("owner UID does not match effective UID %d", uid)
	}

	return nil
}

func (s ReceiptStore) withLock(initialize bool, fn func() error) error {
	if err := s.ensureRoot(initialize); err != nil {
		return err
	}

	lock, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon service receipt lock: %w", err)
	}

	defer func() { _ = lock.Close() }()

	if err := os.Chmod(s.lockPath(), 0o600); err != nil {
		return err
	}

	if err := secureOwnedPath(s.lockPath(), s.UID, false); err != nil {
		return fmt.Errorf("validate daemon service receipt lock: %w", err)
	}

	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock daemon service receipt: %w", err)
	}

	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()

	return fn()
}

func (s ReceiptStore) Load() (Receipt, error) {
	var receipt Receipt

	err := s.withLock(false, func() error {
		var err error

		receipt, err = s.loadLocked()

		return err
	})

	return receipt, err
}

func (s ReceiptStore) loadLocked() (Receipt, error) {
	primaryData, primaryErr := os.ReadFile(s.primaryPath())
	if primaryErr == nil {
		if receipt, err := decodeReceipt(primaryData); err == nil {
			return receipt, nil
		} else {
			primaryErr = err
		}
	}

	backupData, backupErr := os.ReadFile(s.backupPath())
	if backupErr == nil {
		if receipt, err := decodeReceipt(backupData); err == nil {
			// A valid backup is authoritative when the primary is missing or
			// corrupt. Restore it under the same global lock so the next Update
			// cannot be blocked by an invalid primary and so recovery survives a
			// second crash before an explicit repair command.
			if err := atomicWriteFile(s.primaryPath(), backupData, 0o600); err != nil {
				return Receipt{}, fmt.Errorf("restore daemon service receipt from backup: %w", err)
			}

			if err := syncDirectory(s.Root); err != nil {
				return Receipt{}, err
			}

			return receipt, nil
		} else {
			backupErr = err
		}
	}

	if errors.Is(primaryErr, os.ErrNotExist) && errors.Is(backupErr, os.ErrNotExist) {
		return Receipt{}, ErrReceiptMissing
	}

	return Receipt{}, errors.Join(
		fmt.Errorf("load primary daemon service receipt: %w", primaryErr),
		fmt.Errorf("load backup daemon service receipt: %w", backupErr),
	)
}

// InitializeAfterTotalLoss replaces only receipts already proven unusable by
// the caller. Original bytes are retained under explicit .corrupt names for
// diagnosis; they are never silently overwritten with an empty mapping.
func (s ReceiptStore) InitializeAfterTotalLoss(receipt Receipt) error {
	return s.withLock(true, func() error {
		if _, err := s.loadLocked(); err == nil || errors.Is(err, ErrReceiptMissing) {
			if err == nil {
				return errors.New("daemon service receipt is still recoverable")
			}
		} else {
			for _, path := range []string{s.primaryPath(), s.backupPath()} {
				if _, statErr := os.Lstat(path); statErr == nil {
					if renameErr := os.Rename(path, path+".corrupt"); renameErr != nil {
						return renameErr
					}
				} else if !errors.Is(statErr, os.ErrNotExist) {
					return statErr
				}
			}
		}

		receipt.Transaction++

		return s.saveLocked(receipt)
	})
}

// Update performs one crash-safe receipt transaction. initialize must only be
// true after the caller proved none of the 65 exact jobs is registered/live.
func (s ReceiptStore) Update(initialize bool, mutate func(*Receipt) error) (Receipt, error) {
	var result Receipt

	err := s.withLock(initialize, func() error {
		receipt, err := s.loadLocked()
		if errors.Is(err, ErrReceiptMissing) && initialize {
			receipt = NewReceipt()
			err = nil
		}

		if err != nil {
			return err
		}

		if err := mutate(&receipt); err != nil {
			return err
		}

		receipt.Transaction++
		if err := s.saveLocked(receipt); err != nil {
			return err
		}

		result = receipt

		return nil
	})

	return result, err
}

func (s ReceiptStore) saveLocked(receipt Receipt) error {
	checked, err := receipt.withChecksum()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(checked, "", "  ")
	if err != nil {
		return err
	}

	data = append(data, '\n')

	if current, err := os.ReadFile(s.primaryPath()); err == nil {
		if _, err := decodeReceipt(current); err != nil {
			return fmt.Errorf("refuse to replace invalid primary receipt: %w", err)
		}

		if err := atomicWriteFile(s.backupPath(), current, 0o600); err != nil {
			return fmt.Errorf("write receipt backup: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := atomicWriteFile(s.primaryPath(), data, 0o600); err != nil {
		return fmt.Errorf("write receipt: %w", err)
	}

	return syncDirectory(s.Root)
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if errors.Is(err, os.ErrExist) {
		if removeErr := os.Remove(path + ".tmp"); removeErr != nil {
			return errors.Join(err, removeErr)
		}

		tmp, err = os.OpenFile(path+".tmp", os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	}

	if err != nil {
		return err
	}

	tmpPath := tmp.Name()
	ok := false

	defer func() {
		_ = tmp.Close()

		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return err
	}

	if err := tmp.Sync(); err != nil {
		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}

	ok = true

	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()

	return dir.Sync()
}

func ReserveProfile(receipt *Receipt, profile string, uid int, generation string) (Lease, bool, error) {
	receipt.normalize()

	if profile == "" || strings.TrimSpace(profile) != profile {
		return Lease{}, false, errors.New("named daemon service requires a canonical non-empty profile")
	}

	if existing, ok := receipt.Leases[profile]; ok {
		return existing, false, nil
	}

	if receipt.Pending != nil {
		return Lease{}, false, fmt.Errorf("%w for slot %s", ErrPendingInProgress, receipt.Pending.Slot)
	}

	used := make(map[string]bool, len(receipt.Leases)+len(receipt.Quarantined))
	for _, lease := range receipt.Leases {
		used[lease.Slot] = true
	}

	for slot := range receipt.Quarantined {
		used[slot] = true
	}

	for _, definition := range Definitions()[1:] {
		if used[definition.Slot] {
			continue
		}

		lease := Lease{Profile: profile, Slot: definition.Slot, UID: uid, RegisteredGeneration: generation}
		receipt.Pending = &PendingOperation{Kind: "register", Profile: profile, Slot: definition.Slot, UID: uid, Generation: generation}

		return lease, true, nil
	}

	profiles := make([]string, 0, len(receipt.Leases))
	for profile := range receipt.Leases {
		profiles = append(profiles, profile)
	}

	sort.Strings(profiles)

	return Lease{}, false, fmt.Errorf("%w (%d mapped profiles: %s); remove a dormant lease with GRAITH_PROFILE=<name> gr daemon service remove", ErrSlotCapacity, len(profiles), strings.Join(profiles, ", "))
}

func CommitProfile(receipt *Receipt, lease Lease) error {
	if receipt.Pending == nil || receipt.Pending.Kind != "register" || receipt.Pending.Profile != lease.Profile || receipt.Pending.Slot != lease.Slot || receipt.Pending.UID != lease.UID || receipt.Pending.Generation != lease.RegisteredGeneration {
		return errors.New("daemon service pending registration does not match lease")
	}

	receipt.Leases[lease.Profile] = lease
	receipt.Pending = nil

	return nil
}

func ReleaseProfile(receipt *Receipt, profile string, confirmedDownAndUnregistered bool) error {
	if !confirmedDownAndUnregistered {
		return errors.New("refusing to release daemon service lease before confirmed stop and unregister")
	}

	lease, ok := receipt.Leases[profile]
	if !ok {
		return fmt.Errorf("no daemon service lease for profile %q", profile)
	}

	delete(receipt.Leases, profile)
	delete(receipt.Quarantined, lease.Slot)

	return nil
}

func QuarantineSlot(receipt *Receipt, slot, reason string) error {
	if _, err := DefinitionForSlot(slot); err != nil {
		return fmt.Errorf("cannot quarantine slot %q", slot)
	}

	if strings.TrimSpace(reason) == "" {
		return errors.New("quarantine reason is required")
	}

	receipt.normalize()
	receipt.Quarantined[slot] = reason

	return nil
}
