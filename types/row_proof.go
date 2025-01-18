package types

import (
	"errors"
	"fmt"

	"github.com/tendermint/tendermint/crypto/merkle"
	tmbytes "github.com/tendermint/tendermint/libs/bytes"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
)

// RowProof is a Merkle proof that a set of rows exist in a Merkle tree with a
// given data root.
type RowProof struct {
	// RowRoots are the roots of the rows being proven.
	RowRoots []tmbytes.HexBytes `json:"row_roots"`
	// Proofs is a list of Merkle proofs where each proof proves that a row
	// exists in a Merkle tree with a given data root.
	Proofs []*merkle.Proof `json:"proofs"`
	// StartRow the index of the start row.
	// Note: currently, StartRow is not validated as part of the proof verification.
	// If this field is used downstream, Validate(root) should be called along with
	// extra validation depending on how it's used.
	StartRow uint32 `json:"start_row"`
	// EndRow the index of the end row.
	// Note: currently, EndRow is not validated as part of the proof verification.
	// If this field is used downstream, Validate(root) should be called along with
	// extra validation depending on how it's used.
	EndRow uint32 `json:"end_row"`
}

// Validate performs checks on the fields of this RowProof. Returns an error if
// the proof fails validation. If the proof passes validation, this function
// attempts to verify the proof. It returns nil if the proof is valid.
func (rp RowProof) Validate(root []byte) error {
	if rp.EndRow < rp.StartRow {
		return fmt.Errorf("end row %d cannot be less than start row %d", rp.EndRow, rp.StartRow)
	}
	if int(rp.EndRow-rp.StartRow+1) != len(rp.RowRoots) {
		return fmt.Errorf("the number of rows %d must equal the number of row roots %d", int(rp.EndRow-rp.StartRow+1), len(rp.RowRoots))
	}
	if len(rp.Proofs) != len(rp.RowRoots) {
		return fmt.Errorf("the number of proofs %d must equal the number of row roots %d", len(rp.Proofs), len(rp.RowRoots))
	}
	if len(rp.Proofs) == 0 {
		return fmt.Errorf("empty proofs")
	}
	firstProofIndex := rp.Proofs[0].Index
	for i := 1; i < len(rp.Proofs); i++ {
		if rp.Proofs[0].Total != rp.Proofs[i].Total {
			return errors.New("proofs should have the same total")
		}
		if rp.Proofs[i].Index != firstProofIndex+1 {
			return errors.New("proof indexes are not sequential")
		}
		firstProofIndex++
	}
	if int64(rp.StartRow) != rp.Proofs[0].Index {
		return fmt.Errorf("invalid start row")
	}
	if int64(rp.EndRow) != rp.Proofs[len(rp.Proofs)-1].Index {
		return fmt.Errorf("invalid end row")
	}
	if !rp.VerifyProof(root) {
		return errors.New("row proof failed to verify")
	}

	return nil
}

// VerifyProof verifies that all the row roots in this RowProof exist in a
// Merkle tree with the given root. Returns true if all proofs are valid.
func (rp RowProof) VerifyProof(root []byte) bool {
	for i, proof := range rp.Proofs {
		err := proof.Verify(root, rp.RowRoots[i])
		if err != nil {
			return false
		}
	}
	return true
}

func RowProofFromProto(p *tmproto.RowProof) RowProof {
	if p == nil {
		return RowProof{}
	}
	rowRoots := make([]tmbytes.HexBytes, len(p.RowRoots))
	rowProofs := make([]*merkle.Proof, len(p.Proofs))
	for i := range p.Proofs {
		rowRoots[i] = p.RowRoots[i]
		rowProofs[i] = &merkle.Proof{
			Total:    p.Proofs[i].Total,
			Index:    p.Proofs[i].Index,
			LeafHash: p.Proofs[i].LeafHash,
			Aunts:    p.Proofs[i].Aunts,
		}
	}

	return RowProof{
		RowRoots: rowRoots,
		Proofs:   rowProofs,
		StartRow: p.StartRow,
		EndRow:   p.EndRow,
	}
}
