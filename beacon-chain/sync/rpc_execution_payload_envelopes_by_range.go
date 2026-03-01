package sync

import (
	"context"
	"math"
	"time"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/p2p"
	p2ptypes "github.com/OffchainLabs/prysm/v7/beacon-chain/p2p/types"
	"github.com/OffchainLabs/prysm/v7/config/params"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing/trace"
	engpb "github.com/OffchainLabs/prysm/v7/proto/engine/v1"
	pb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/OffchainLabs/prysm/v7/time/slots"
	libp2pcore "github.com/libp2p/go-libp2p/core"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

var envelopeRpcThrottleInterval = time.Second

// executionPayloadEnvelopesByRangeRPCHandler looks up the request execution payload envelopes from
// the database for the given slot range, serving one envelope per canonical block slot.
func (s *Service) executionPayloadEnvelopesByRangeRPCHandler(ctx context.Context, msg any, stream libp2pcore.Stream) error {
	ctx, span := trace.StartSpan(ctx, "sync.ExecutionPayloadEnvelopesByRangeHandler")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, respTimeout)
	defer cancel()
	SetRPCStreamDeadlines(stream)
	log := log.WithField("handler", p2p.ExecutionPayloadEnvelopesByRangeName[1:])

	r, ok := msg.(*pb.ExecutionPayloadEnvelopesByRangeRequest)
	if !ok {
		return errors.New("message is not type *pb.ExecutionPayloadEnvelopesByRangeRequest")
	}
	if err := s.rateLimiter.validateRequest(stream, 1); err != nil {
		return err
	}

	remotePeer := stream.Conn().RemotePeer()

	log.WithFields(logrus.Fields{
		"startSlot": r.StartSlot,
		"count":     r.Count,
		"peer":      remotePeer,
	}).Debug("Serving execution payload envelopes by range request")

	rp, err := validateEnvelopesByRange(r, s.cfg.clock.CurrentSlot())
	if err != nil {
		s.writeErrorResponseToStream(responseCodeInvalidRequest, err.Error(), stream)
		s.downscorePeer(remotePeer, "executionPayloadEnvelopesByRangeRPCHandlerValidationError")
		tracing.AnnotateError(span, err)
		return err
	}
	available := s.validateRangeAvailability(rp)
	if !available {
		log.WithFields(logrus.Fields{
			"startSlot": rp.start,
			"endSlot":   rp.end,
			"size":      rp.size,
			"current":   s.cfg.clock.CurrentSlot(),
		}).Debug("Error in validating range availability for envelopes")
		s.writeErrorResponseToStream(responseCodeResourceUnavailable, p2ptypes.ErrResourceUnavailable.Error(), stream)
		tracing.AnnotateError(span, err)
		return nil
	}

	// Ticker to stagger out large requests.
	ticker := time.NewTicker(envelopeRpcThrottleInterval)
	defer ticker.Stop()
	batcher, err := newBlockRangeBatcher(rp, s.cfg.beaconDB, s.rateLimiter, s.cfg.chain.IsCanonical, ticker)
	if err != nil {
		log.WithError(err).Error("Cannot create new block range batcher for envelopes")
		s.writeErrorResponseToStream(responseCodeServerError, p2ptypes.ErrGeneric.Error(), stream)
		tracing.AnnotateError(span, err)
		return err
	}

	var batch blockBatch
	var more bool
	// wQuota caps total envelopes sent per request, bounded by MAX_REQUEST_PAYLOADS.
	wQuota := params.BeaconConfig().MaxRequestPayloads
	for batch, more = batcher.next(ctx, stream); more; batch, more = batcher.next(ctx, stream) {
		wQuota, err = s.streamEnvelopeBatch(ctx, batch, wQuota, stream)
		if err != nil {
			return err
		}
		if wQuota == 0 {
			break
		}
	}

	if err := batch.error(); err != nil {
		log.WithError(err).Debug("Error in ExecutionPayloadEnvelopesByRange batch")
		if !errors.Is(err, p2ptypes.ErrRateLimited) {
			s.writeErrorResponseToStream(responseCodeServerError, p2ptypes.ErrGeneric.Error(), stream)
		}
		tracing.AnnotateError(span, err)
		return err
	}

	closeStream(stream, log)
	return nil
}

// streamEnvelopeBatch sends all available envelopes for the canonical blocks in the batch.
// It returns the remaining write quota and any error encountered.
func (s *Service) streamEnvelopeBatch(ctx context.Context, batch blockBatch, wQuota uint64, stream libp2pcore.Stream) (uint64, error) {
	if wQuota == 0 {
		return 0, nil
	}
	_, span := trace.StartSpan(ctx, "sync.streamEnvelopeBatch")
	defer span.End()

	for _, b := range batch.canonical() {
		root := b.Root()
		if !s.cfg.beaconDB.HasExecutionPayloadEnvelope(ctx, root) {
			// No envelope for this slot — spec allows gaps, just skip.
			continue
		}
		blindedEnv, err := s.cfg.beaconDB.ExecutionPayloadEnvelope(ctx, root)
		if err != nil {
			s.writeErrorResponseToStream(responseCodeServerError, p2ptypes.ErrGeneric.Error(), stream)
			return wQuota, errors.Wrapf(err, "could not retrieve execution payload envelope for root %#x", root)
		}

		// TODO: unblind the envelope by fetching the full execution payload from the EL.
		fullEnv := &pb.SignedExecutionPayloadEnvelope{
			Message: &pb.ExecutionPayloadEnvelope{
				Payload: &engpb.ExecutionPayloadDeneb{
					ParentHash:    make([]byte, 32),
					FeeRecipient:  make([]byte, 20),
					StateRoot:     make([]byte, 32),
					ReceiptsRoot:  make([]byte, 32),
					LogsBloom:     make([]byte, 256),
					PrevRandao:    make([]byte, 32),
					BaseFeePerGas: make([]byte, 32),
					BlockHash:     blindedEnv.Message.BlockHash,
				},
				ExecutionRequests: blindedEnv.Message.ExecutionRequests,
				BuilderIndex:      blindedEnv.Message.BuilderIndex,
				BeaconBlockRoot:   blindedEnv.Message.BeaconBlockRoot,
				Slot:              blindedEnv.Message.Slot,
				StateRoot:         blindedEnv.Message.StateRoot,
			},
			Signature: blindedEnv.Signature,
		}

		SetStreamWriteDeadline(stream, defaultWriteDuration)
		if chunkErr := WriteExecutionPayloadEnvelopeChunk(stream, s.cfg.clock, s.cfg.p2p.Encoding(), fullEnv); chunkErr != nil {
			log.WithError(chunkErr).Debug("Could not send execution payload envelope chunk")
			s.writeErrorResponseToStream(responseCodeServerError, p2ptypes.ErrGeneric.Error(), stream)
			tracing.AnnotateError(span, chunkErr)
			return wQuota, chunkErr
		}
		s.rateLimiter.add(stream, 1)
		wQuota -= 1
		if wQuota == 0 {
			return 0, nil
		}
	}
	return wQuota, nil
}

// validateEnvelopesByRange validates the ExecutionPayloadEnvelopesByRange request and returns
// normalized rangeParams. Mirrors validateBlobsByRange in structure.
func validateEnvelopesByRange(r *pb.ExecutionPayloadEnvelopesByRangeRequest, current primitives.Slot) (rangeParams, error) {
	if r.Count == 0 {
		return rangeParams{}, errors.Wrap(p2ptypes.ErrInvalidRequest, "invalid request Count parameter")
	}
	rp := rangeParams{
		start: r.StartSlot,
		size:  r.Count,
	}
	// Peers may overshoot the current slot when in initial sync — treat as noop rather than error.
	if rp.start > current {
		return rangeParams{start: current, end: current, size: 0}, nil
	}

	var err error
	rp.end, err = rp.start.SafeAdd(rp.size - 1)
	if err != nil {
		return rangeParams{}, errors.Wrap(p2ptypes.ErrInvalidRequest, "overflow start + count - 1")
	}

	maxRequest := params.BeaconConfig().MaxRequestPayloads
	maxStart, err := current.SafeAdd(maxRequest * 2)
	if err != nil {
		return rangeParams{}, errors.Wrap(p2ptypes.ErrInvalidRequest, "current + maxRequest * 2 > max uint")
	}
	if rp.start > maxStart {
		return rangeParams{}, errors.Wrap(p2ptypes.ErrInvalidRequest, "start > maxStart")
	}

	// Envelopes only exist from the Gloas fork onward — clamp start if needed.
	if params.BeaconConfig().GloasForkEpoch != math.MaxUint64 {
		gloasStart, err := slots.EpochStart(params.BeaconConfig().GloasForkEpoch)
		if err != nil {
			return rangeParams{}, errors.Wrap(p2ptypes.ErrInvalidRequest, "could not compute Gloas fork start slot")
		}
		if rp.start < gloasStart {
			rp.start = gloasStart
		}
	}

	if rp.end > current {
		rp.end = current
	}
	if rp.end < rp.start {
		rp.end = rp.start
	}
	if rp.size > maxRequest {
		rp.size = maxRequest
	}

	return rp, nil
}
