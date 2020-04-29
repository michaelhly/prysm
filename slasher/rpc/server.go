package rpc

import (
	"context"

	"github.com/pkg/errors"
	ethpb "github.com/prysmaticlabs/ethereumapis/eth/v1alpha1"
	slashpb "github.com/prysmaticlabs/prysm/proto/slashing"
	"github.com/prysmaticlabs/prysm/shared/attestationutil"
	"github.com/prysmaticlabs/prysm/shared/bls"
	"github.com/prysmaticlabs/prysm/shared/p2putils"
	"github.com/prysmaticlabs/prysm/slasher/beaconclient"
	"github.com/prysmaticlabs/prysm/slasher/db"
	"github.com/prysmaticlabs/prysm/slasher/detection"
	log "github.com/sirupsen/logrus"
	"go.opencensus.io/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server defines a server implementation of the gRPC Slasher service,
// providing RPC endpoints for retrieving slashing proofs for malicious validators.
type Server struct {
	ctx          context.Context
	detector     *detection.Service
	slasherDB    db.Database
	beaconClient *beaconclient.Service
}

// IsSlashableAttestation returns an attester slashing if the attestation submitted
// is a slashable vote.
func (ss *Server) IsSlashableAttestation(ctx context.Context, req *ethpb.IndexedAttestation) (*slashpb.AttesterSlashingResponse, error) {
	ctx, span := trace.StartSpan(ctx, "detection.IsSlashableAttestation")
	defer span.End()
	if req == nil {
		return nil, errors.New("nil indexed attestation")
	}
	indices := req.AttestingIndices
	fork, err := p2putils.Fork(req.Data.Target.Epoch)
	if err != nil {
		return nil, err
	}
	pkMap, err := ss.beaconClient.FindOrGetPublicKeys(ctx, indices)
	if err != nil {
		return nil, err
	}
	pubkeys := []*bls.PublicKey{}
	for _, pkBytes := range pkMap {
		pk, err := bls.PublicKeyFromBytes(pkBytes[:])
		if err != nil {
			return nil, errors.Wrap(err, "could not deserialize validator public key")
		}
		pubkeys = append(pubkeys, pk)
	}
	gvr, err := ss.beaconClient.GenesisValidatorsRoot(ctx)
	if err != nil {
		return nil, err
	}
	err = attestationutil.VerifyIndexedAttestation(ctx, req, pubkeys, gvr, fork)
	if err != nil {
		log.WithError(err).Error("Failed to verify indexed attestation signature")
		return nil, status.Errorf(codes.Internal, "Could not verify indexed attestation signature: %v: %v", req, err)
	}

	if err := ss.slasherDB.SaveIndexedAttestation(ctx, req); err != nil {
		log.WithError(err).Error("Could not save indexed attestation")
		return nil, status.Errorf(codes.Internal, "Could not save indexed attestation: %v: %v", req, err)
	}
	slashings, err := ss.detector.DetectAttesterSlashings(ctx, req)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Could not detect attester slashings for attestation: %v: %v", req, err)
	}
	if len(slashings) < 1 {
		if err := ss.detector.UpdateSpans(ctx, req); err != nil {
			log.WithError(err).Error("Could not update spans")
		}
	}
	return &slashpb.AttesterSlashingResponse{
		AttesterSlashing: slashings,
	}, nil
}

// IsSlashableBlock returns an proposer slashing if the block submitted
// is a double proposal.
func (ss *Server) IsSlashableBlock(ctx context.Context, req *ethpb.SignedBeaconBlockHeader) (*slashpb.ProposerSlashingResponse, error) {
	return nil, errors.New("unimplemented")
}
