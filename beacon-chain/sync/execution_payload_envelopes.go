package sync

import (
	"context"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/blockchain"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/db"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/p2p"
	p2ptypes "github.com/OffchainLabs/prysm/v7/beacon-chain/p2p/types"
	"github.com/OffchainLabs/prysm/v7/consensus-types/blocks"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/OffchainLabs/prysm/v7/runtime/version"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// ExecutionPayloadEnvelopesParams holds the dependencies needed to fetch and
// receive execution payload envelopes from peers.
type ExecutionPayloadEnvelopesParams struct {
	// P2P is the P2P network provider for sending RPC requests.
	P2P p2p.P2P
	// Tor is used to resolve the current slot for topic/context-byte selection.
	Tor blockchain.TemporalOracle
	// CtxMap maps fork digests to fork version integers, used when decoding
	// chunked envelope responses.
	CtxMap ContextByteVersions
	// BeaconDB is used to check whether an envelope already exists locally.
	BeaconDB db.ReadOnlyDatabase
	// Chain is the blockchain service that processes received envelopes.
	Chain blockchain.ExecutionPayloadEnvelopeReceiver
}

// FetchExecutionPayloadEnvelopes fetches execution payload envelopes for the
// provided canonical Gloas-era blocks from a peer and processes each one via
// ReceiveExecutionPayloadEnvelope.
//
// The function skips blocks that already have an envelope in the local DB and
// blocks that are not yet in the Gloas fork.  For the remaining blocks it sends
// a single ExecutionPayloadEnvelopesByRoot RPC request to `pid` and calls
// ReceiveExecutionPayloadEnvelope for every envelope that comes back.
//
// This is the P2P RPC pull-path complement to the gossip path handled by
// pending_payload_envelope.go.
func FetchExecutionPayloadEnvelopes(
	ctx context.Context,
	params ExecutionPayloadEnvelopesParams,
	roBlocks []blocks.ROBlock,
	pid peer.ID,
) error {
	if len(roBlocks) == 0 {
		return nil
	}

	// Collect roots of blocks that need an envelope.
	req := make(p2ptypes.ExecutionPayloadEnvelopesByRootReq, 0, len(roBlocks))
	for _, b := range roBlocks {
		// Only Gloas-fork blocks have execution payload envelopes.
		if b.Version() < version.Gloas {
			continue
		}
		root := b.Root()
		if params.BeaconDB.HasExecutionPayloadEnvelope(ctx, root) {
			// Already have it locally.
			continue
		}
		req = append(req, root)
	}

	if len(req) == 0 {
		return nil
	}

	log.WithFields(logrus.Fields{
		"peer":  pid,
		"count": len(req),
	}).Debug("Fetching missing execution payload envelopes by root")

	envelopes, err := SendExecutionPayloadEnvelopesByRootRequest(
		ctx, params.Tor, params.P2P, pid, params.CtxMap, &req,
	)
	if err != nil {
		return errors.Wrap(err, "send execution payload envelopes by root request")
	}

	for _, env := range envelopes {
		if err := receiveEnvelope(ctx, params.Chain, env); err != nil {
			// Log and continue — a single bad envelope should not abort the batch.
			log.WithError(err).Debug("Could not receive fetched execution payload envelope")
		}
	}

	return nil
}

// FetchExecutionPayloadEnvelopesByRange fetches execution payload envelopes for
// the slot range [startSlot, startSlot+count) from `pid` and processes each one
// via ReceiveExecutionPayloadEnvelope.
//
// This is the range-based counterpart to FetchExecutionPayloadEnvelopes and is
// suitable for bulk fetching during initial sync.
func FetchExecutionPayloadEnvelopesByRange(
	ctx context.Context,
	params ExecutionPayloadEnvelopesParams,
	pid peer.ID,
	startSlot primitives.Slot,
	count uint64,
) error {
	if count == 0 {
		return nil
	}

	req := &ethpb.ExecutionPayloadEnvelopesByRangeRequest{
		StartSlot: startSlot,
		Count:     count,
	}

	log.WithFields(logrus.Fields{
		"peer":      pid,
		"startSlot": startSlot,
		"count":     count,
	}).Debug("Fetching execution payload envelopes by range")

	envelopes, err := SendExecutionPayloadEnvelopesByRangeRequest(
		ctx, params.Tor, params.P2P, pid, params.CtxMap, req,
	)
	if err != nil {
		return errors.Wrap(err, "send execution payload envelopes by range request")
	}

	for _, env := range envelopes {
		if err := receiveEnvelope(ctx, params.Chain, env); err != nil {
			log.WithError(err).Debug("Could not receive fetched execution payload envelope (range)")
		}
	}

	return nil
}

// receiveEnvelope wraps a proto envelope in the read-only interface and calls
// ReceiveExecutionPayloadEnvelope on the blockchain service.
func receiveEnvelope(
	ctx context.Context,
	chain blockchain.ExecutionPayloadEnvelopeReceiver,
	env *ethpb.SignedExecutionPayloadEnvelope,
) error {
	if env == nil || env.Message == nil {
		return errors.New("nil or empty execution payload envelope")
	}
	wrapped, err := blocks.WrappedROSignedExecutionPayloadEnvelope(env)
	if err != nil {
		return errors.Wrap(err, "wrap signed execution payload envelope")
	}
	return chain.ReceiveExecutionPayloadEnvelope(ctx, wrapped)
}
