package authority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/JayWanX/memory/internal/agency"
)

// MaxArtifactBytes matches the existing Agency CAS object bound. R7 stores
// one verified object per ref in T0; it does not introduce a second chunking or
// manifest model.
const MaxArtifactBytes = 4 << 20

// ArtifactVerifier is the narrow, neutral seam to immutable CAS bytes. A
// successful call means the exact object was read within maximum bytes and
// hashed to digest during this admission attempt; catalog presence alone is
// never sufficient.
type ArtifactVerifier interface {
	VerifyArtifact(context.Context, agency.Digest, int64) error
}

type ArtifactVerifierFunc func(context.Context, agency.Digest, int64) error

func (verify ArtifactVerifierFunc) VerifyArtifact(ctx context.Context, digest agency.Digest,
	byteSize int64,
) error {
	if verify == nil {
		return errors.New("verify Artifact: nil verifier")
	}
	return verify(ctx, digest, byteSize)
}

// VerifiedArtifact is created only by hashing the exact bytes. The catalog
// records availability metadata; bytes remain in the external CAS.
type VerifiedArtifact struct {
	digest     agency.Digest
	byteSize   int64
	verifiedAt time.Time
}

func VerifyArtifact(content []byte, verifiedAt time.Time) (VerifiedArtifact, error) {
	if len(content) > MaxArtifactBytes {
		return VerifiedArtifact{}, fmt.Errorf("verify Artifact: %d bytes exceeds %d", len(content), MaxArtifactBytes)
	}
	verifiedAt = verifiedAt.Round(0).UTC()
	if verifiedAt.IsZero() {
		return VerifiedArtifact{}, errors.New("verify Artifact: zero verification time")
	}
	return VerifiedArtifact{digest: agency.Sum(content), byteSize: int64(len(content)), verifiedAt: verifiedAt}, nil
}

func (artifact VerifiedArtifact) Digest() agency.Digest { return artifact.digest }
func (artifact VerifiedArtifact) ByteSize() int64       { return artifact.byteSize }
func (artifact VerifiedArtifact) VerifiedAt() time.Time { return artifact.verifiedAt }

// CatalogArtifact makes a hash-verified CAS object eligible for Event pins.
// Reverification preserves the first durable verification time.
func (s *Store) CatalogArtifact(ctx context.Context, artifact VerifiedArtifact) error {
	if ctx == nil || artifact.digest.IsZero() || artifact.byteSize < 0 ||
		artifact.byteSize > MaxArtifactBytes || artifact.verifiedAt.IsZero() {
		return errors.New("catalog Artifact: incomplete or out-of-bound Artifact")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("catalog Artifact: begin: %w", err)
	}
	defer tx.Rollback()
	var byteSize int64
	err = tx.QueryRowContext(ctx, "SELECT byte_size FROM verified_artifacts WHERE digest = ?",
		artifact.digest.String()).Scan(&byteSize)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO verified_artifacts(digest, byte_size, verified_at)
			VALUES(?, ?, ?)`, artifact.digest.String(), artifact.byteSize, formatTime(artifact.verifiedAt))
		if err != nil {
			return fmt.Errorf("catalog Artifact: insert: %w", err)
		}
	case err != nil:
		return fmt.Errorf("catalog Artifact: inspect replay: %w", err)
	case byteSize != artifact.byteSize:
		return errors.New("catalog Artifact: digest metadata conflicts with durable catalog")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("catalog Artifact: commit: %w", err)
	}
	return nil
}

func requireArtifacts(ctx context.Context, tx *sql.Tx, digests []agency.Digest) error {
	for _, digest := range digests {
		var present int
		if err := tx.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM verified_artifacts WHERE digest = ?)",
			digest.String()).Scan(&present); err != nil {
			return fmt.Errorf("require verified Artifact: %w", err)
		}
		if present != 1 {
			return ErrArtifactUnavailable
		}
	}
	return nil
}
