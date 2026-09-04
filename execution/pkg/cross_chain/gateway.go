package cross_chain

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"golang.org/x/crypto/sha3"
)

const (
	// MaxHopCount is the maximum allowable routing hops for cross-chain messages (Section 2.6.2 & 11.3).
	MaxHopCount uint8 = 6
)

var (
	ErrHopCountExceeded              = errors.New("hop count exceeds maximum limit of 6")
	ErrUnknownSourceChain            = errors.New("unknown source chain ID")
	ErrEpochMismatch                 = errors.New("epoch mismatch for source chain")
	ErrAllocationExceeded            = errors.New("aggregate amount exceeds source chain allocation ceiling (Scenario 10.7)")
	ErrQuorumNotReached              = errors.New("BFT quorum stake threshold not reached")
	ErrCommitNotAttested             = errors.New("commit root has not been attested by source chain")
	ErrInvalidMerkleProof            = errors.New("invalid Merkle proof")
	ErrAlreadyClaimed                = errors.New("message has already been claimed or processed (idempotent guard)")
	ErrInvalidRefundState            = errors.New("cannot refund message: message is not in Pending status")
	ErrInvalidRefundProof            = errors.New("invalid failed execution proof for refund")
	ErrChainNotDead                  = errors.New("target chain has not been declared dead")
	ErrDeadChainAlreadyClaimed       = errors.New("account balance on dead chain has already been claimed")
	ErrNoActiveContext               = errors.New("no active cross-chain execution context")
	ErrNotCalledByGateway            = errors.New("caller is not authorized by GatewayPrecompile")
	ErrInvalidBLSSignature           = errors.New("BLS Quorum Certificate signature is invalid or empty")
	ErrReserveChainNotConfigured     = errors.New("this chain's ReserveChainID is not configured — cannot mint genesis supply or attest a non-Reserve chain's ceiling-enforced commit")
	ErrOnlyReserveMayMint            = errors.New("ProposalAllocateSupply may only grant allocation to this chain's own configured ReserveChainID")
	ErrGenesisAlreadyMinted          = errors.New("genesis total supply has already been minted once — ProposalAllocateSupply is a one-time genesis operation, not a repeatable mint")
	ErrNonReserveCeilingAttestation  = errors.New("only the configured Reserve chain may perform a ceiling-enforced attestCommit of a nonzero-value commit from another chain")
	ErrInsufficientRegistrationStake = errors.New("ProposalRegisterChain: this chain ID does not yet hold MinRegistrationStake in PerChainAllocation — fund it first via ProposalTransferAllocation from an already-active chain or the Reserve")
	ErrChainAlreadyRegistered        = errors.New("RegisterChainViaStake: this chain ID is already in ChainRegistry -- use ProposalUpdateCommittee to change an existing chain's committee")
)

// OutboundParams contains user/contract request parameters for outbound cross-chain messages.
type OutboundParams struct {
	DestChainID uint64         `json:"dest_chain_id"`
	Target      common.Address `json:"target"`
	Payload     []byte         `json:"payload"`
	AssetID     *big.Int       `json:"asset_id"`
	Value       *big.Int       `json:"value"`
	Tip         *big.Int       `json:"tip"`
	GasFee      *big.Int       `json:"gas_fee"`
	HopCount    uint8          `json:"hop_count"`
	Ordered     bool           `json:"ordered"`
}

// CrossChainContext stores execution context accessible via GetOriginalSender / IsCalledByGateway.
type CrossChainContext struct {
	OriginalSender common.Address `json:"original_sender"`
	SourceChainID  uint64         `json:"source_chain_id"`
	IsGateway      bool           `json:"is_gateway"`
}

// AllocationRejectedListener defines a hook triggered when a chain overdraws its allocation ceiling.
type AllocationRejectedListener func(chainID uint64, requested, available *big.Int)

// GatewayEngine implements the native light-client bridge state machine (Section 2.1 & 2.2).
type GatewayEngine struct {
	mu                         sync.RWMutex
	LocalChainID               uint64
	ChainRegistry              map[uint64]ChainRegistry
	SupplyLedger               *GlobalSupplyLedger
	AttestedCommits            map[string]AttestedCommit
	MessageStatus              map[common.Hash]MessageStatus
	DeadChains                 map[uint64]bool
	DeadChainClaimed           map[string]bool
	ActiveContext              *CrossChainContext
	LockedTips                 map[common.Hash]*big.Int
	ChannelSequence            map[string]uint64
	RelayerBalances            map[common.Address]*big.Int
	allocationRejectedListener AllocationRejectedListener

	// PendingCommitteeAttestations collects individual BLS signature shares for a pending
	// CommitteeUpdate, keyed by "sourceChainId:oldEpoch:payloadHashHex" (Milestone C). Cleared
	// once the corresponding committeeUpdate() succeeds.
	PendingCommitteeAttestations map[string][]CommitteeAttestationShare
	// PendingCommitAttestations collects individual BLS signature shares for a pending
	// commit root attestation, keyed by "sourceChainId:epoch:commitRootHex" (Milestone F).
	PendingCommitAttestations map[string][]CommitAttestationShare

	// PendingOutboundMessages queues real outbound() messages (their sender already validated
	// and their Value/Tip/GasFee already burned/locked) not yet batched into a commit root for
	// BLS attestation -- the missing link between Outbound() and Milestone F's
	// CommitAttestationWorker, which previously had no real trigger (see BatchOutboundCommit's
	// doc comment). Keyed by DestChainID: a commit's single AggregateValueLeaf debits
	// per_chain_allocation[LocalChainID] exactly once per BatchOutboundCommit call, so batching
	// messages bound for DIFFERENT destination chains into the same commit would let each
	// destination's own independent attestCommit() call debit the SAME source allocation
	// against its own local ledger copy -- an over-mint risk across destinations. Scoping the
	// queue (and therefore every batch) to one destination at a time avoids that entirely, and
	// matches ChannelSequence's existing "LocalChainID:DestChainID" pairing convention.
	PendingOutboundMessages map[uint64][]CrossChainMessage `json:"pending_outbound_messages,omitempty"`
	// CommittedBatches records the exact message set (and the epoch it was signed under) behind
	// every commitRoot BatchOutboundCommit has ever produced, keyed by commitRoot. commitRoot is
	// a pure function of the message list (BuildCommitTree), so this is the only state needed
	// for anyone -- this chain's own CommitAttestationWorker, or a relayer, including after a
	// restart -- to deterministically rebuild the identical Merkle tree/proofs later.
	CommittedBatches map[common.Hash]CommittedOutboundBatch `json:"committed_batches,omitempty"`
	// RegisteredPops is a durable, permissionless Proof-of-Possession registry keyed by
	// hex(pubkeyBls) (Milestone C) — see registerCommitteePop()/getRegisteredPop() in
	// gateway_handler.go. Independent of ChainRegistry membership: anyone may register a PoP for
	// their own key at any time.
	RegisteredPops map[string][]byte
	// Governance manages on-chain multi-chain voting and 72h timelocks (Milestone G).
	Governance *GovernanceEngine `json:"governance,omitempty"`
	// AssetRegistry manages custom cross-chain tokens and wrapped assets (Milestone G).
	AssetRegistry *AssetRegistryEngine `json:"asset_registry,omitempty"`

	// ReserveChainID identifies which registered chain is the system's unconditional issuer
	// ("Reserve", Section 2.3) — the only chain allowed to (a) receive the one-time genesis
	// supply mint via ProposalAllocateSupply, and (b) perform a ceiling-enforced AttestCommit
	// of a nonzero-value commit from ANY other chain. Set once from config (config.CrossChain
	// .ReserveChainID, gateway_handler.go's applyReserveChainIDConfig) — never governance-
	// settable. Zero means "not configured" and
	// fails closed on both operations above (see GrantAllocation call site and
	// attestCommitInternal) rather than silently falling back to the old, unrestricted
	// behavior — found 2026-08-27 that neither restriction existed at all before this field:
	// (1) ProposalAllocateSupply could mint fresh GenesisTotalSupply to ANY chain, repeatedly,
	// via nothing but a governance vote (a real Sybil-mintable path, distinct from
	// ClaimMessage's safe transfer-based auto-credit); (2) any non-Reserve chain could call
	// the plain (ceiling-enforced) AttestCommit for ANY other non-Reserve chain's commit,
	// decrementing only ITS OWN local copy of that source's allocation (GlobalSupplyLedger is
	// per-chain-local state) with no cross-chain synchronization — since "route value through
	// Reserve" was previously enforced only by relayer.go's own convention (RelayerDaemon's
	// DaemonConfig.ReserveChainID), never by the GatewayEngine itself. See
	// note/cross_chain_attack_scenario_catalog.md items C7/C8 for the full analysis.
	ReserveChainID uint64 `json:"reserve_chain_id,omitempty"`

	// MinRegistrationStake — C6 mitigation (Sybil chain registration, 2026-08-27, see
	// note/cross_chain_attack_scenario_catalog.md item C6), gating ONLY
	// ExecuteGovernanceProposal's ProposalRegisterChain case (the vote-gated path). Registration
	// there was previously fully decoupled from SupplyLedger — a new chain could be voted into
	// ChainRegistry, and therefore gain 1 full governance vote, while holding zero allocation.
	// When set (>0), ProposalRegisterChain additionally requires
	// SupplyLedger.PerChainAllocation[reg.ChainID] >= MinRegistrationStake at execution time —
	// the candidate chain ID must already have been pre-funded via ProposalTransferAllocation
	// from an existing active chain (or the Reserve) BEFORE the registration proposal can
	// execute. TransferAllocation/SetInitialAllocation have no ChainRegistry membership check
	// (confirmed by direct code reading), so pre-funding a not-yet-registered chain ID works
	// today with no other change needed. Zero/nil (the default, and every pre-2026-08-27 config)
	// preserves the exact old permissionless-registration behavior for this vote-gated path.
	// Does NOT gate RegisterChainViaStake (2026-08-28: that vote-free path now runs on a
	// completely different instrument — a REAL native-coin wallet deposit, checked+burned by
	// gateway_handler.go against engine.MinNativeStakeToRegister — see RegisterChainViaStake's
	// own doc comment). Set once from config (config.CrossChain.MinRegistrationStakeWei,
	// gateway_handler.go's applyMinRegistrationStakeConfig) — never governance-settable, matching
	// ReserveChainID's own pattern.
	MinRegistrationStake *big.Int `json:"min_registration_stake,omitempty"`

	// MinNativeStakeToRegister is the real, liquid native-coin (Root Anchor's own base asset —
	// deliberately NOT an ERC-20-style token, and NOT PerChainAllocation) minimum wallet balance
	// gateway_handler.go's "registerChainViaStake" case requires tx.FromAddress() to hold before
	// it will call RegisterChainViaStake, then moves exactly this amount out of that real wallet
	// into GATEWAY_CONTRACT_ADDRESS as a permanent, held on-chain deposit (2026-08-28 user
	// request: "dùng tiền từ ví từ tài khoản thật làm điều kiện khởi tạo private chain ... không
	// phải loại token erc 20 gì cả" -- use real wallet money as the founding condition, not any
	// ERC-20-style token). GatewayEngine itself has no AccountStateDB access, so this field is
	// only a threshold record — the actual balance check and the move happen one layer up, in
	// gateway_handler.go (which does have AccountStateDB access), as a burn-then-mint pair
	// (debit tx.FromAddress(), credit GATEWAY_CONTRACT_ADDRESS) — the same total-supply-
	// conserving primitive pair "outbound"/"claimMessage" already use for cross-chain value
	// transfer, just both legs landing on this same chain here (a plain burn call alone only
	// debits `from` and credits nowhere — see gateway_handler.go's "registerChainViaStake" case
	// for why the mint leg is required). Set once from config
	// (config.CrossChain.MinNativeStakeToRegisterWei, gateway_handler.go's
	// applyMinNativeStakeToRegisterConfig) — never governance-settable, and, unlike
	// MinRegistrationStake, REQUIRED (not opt-in): with BootstrapFoundingChains retired,
	// RegisterChainViaStake is the only vote-free registration path left, so an unconfigured
	// minimum here must fail closed rather than silently reopening permissionless Sybil
	// registration for every chain, founding or not.
	MinNativeStakeToRegister *big.Int `json:"min_native_stake_to_register,omitempty"`

	// GovernanceTimelockSecondsOverride — set only via ApplyGovernanceTimelockOverride(), from
	// an explicit devnet-only config field (config.CrossChainConfig
	// .DevnetGovernanceTimelockSecondsOverride). Zero (the value on every real chain's
	// persisted state) means "use the real 72h DefaultGovernanceTimelockSeconds" — see that
	// field's own doc comment for why this exists.
	GovernanceTimelockSecondsOverride uint64 `json:"governance_timelock_seconds_override,omitempty"`
}

// ApplyGovernanceTimelockOverride is a no-op when seconds==0 (every real production config).
// When explicitly set (devnet/testing only — see GovernanceTimelockSecondsOverride's own doc
// comment), it both records the override for future EnsureGovernance() calls and, if a
// Governance engine already exists (either freshly constructed by NewGatewayEngine or just
// deserialized), updates its TimelockDelaySeconds directly so the change takes effect
// immediately rather than only on the next from-scratch construction.
func (g *GatewayEngine) ApplyGovernanceTimelockOverride(seconds uint64) {
	if seconds == 0 {
		return
	}
	g.GovernanceTimelockSecondsOverride = seconds
	if g.Governance != nil {
		g.Governance.TimelockDelaySeconds = seconds
	}
}

// NewGatewayEngine initializes a new GatewayEngine instance for the local chain.
func NewGatewayEngine(
	localChainID uint64,
	registry map[uint64]ChainRegistry,
	ledger *GlobalSupplyLedger,
) *GatewayEngine {
	activeChains := make([]uint64, 0, len(registry))
	for c := range registry {
		activeChains = append(activeChains, c)
	}
	gov := NewGovernanceEngine(activeChains)
	assetReg := NewAssetRegistryEngine(registry, gov)

	return &GatewayEngine{
		LocalChainID:                 localChainID,
		ChainRegistry:                registry,
		SupplyLedger:                 ledger,
		AttestedCommits:              make(map[string]AttestedCommit),
		MessageStatus:                make(map[common.Hash]MessageStatus),
		DeadChains:                   make(map[uint64]bool),
		DeadChainClaimed:             make(map[string]bool),
		LockedTips:                   make(map[common.Hash]*big.Int),
		ChannelSequence:              make(map[string]uint64),
		RelayerBalances:              make(map[common.Address]*big.Int),
		PendingCommitteeAttestations: make(map[string][]CommitteeAttestationShare),
		PendingCommitAttestations:    make(map[string][]CommitAttestationShare),
		PendingOutboundMessages:      make(map[uint64][]CrossChainMessage),
		CommittedBatches:             make(map[common.Hash]CommittedOutboundBatch),
		RegisteredPops:               make(map[string][]byte),
		Governance:                   gov,
		AssetRegistry:                assetReg,
	}
}

// EnsureGovernance ensures Governance and AssetRegistry engines are initialized after JSON deserialization.
func (g *GatewayEngine) EnsureGovernance() {
	if g.Governance == nil {
		activeChains := make([]uint64, 0, len(g.ChainRegistry))
		for c := range g.ChainRegistry {
			activeChains = append(activeChains, c)
		}
		if g.GovernanceTimelockSecondsOverride > 0 {
			g.Governance = NewGovernanceEngineWithTimelock(activeChains, g.GovernanceTimelockSecondsOverride)
		} else {
			g.Governance = NewGovernanceEngine(activeChains)
		}
	}
	if g.AssetRegistry == nil {
		g.AssetRegistry = NewAssetRegistryEngine(g.ChainRegistry, g.Governance)
	} else {
		g.AssetRegistry.ChainRegistry = g.ChainRegistry
		g.AssetRegistry.Governance = g.Governance
	}
}

func (g *GatewayEngine) WithdrawRelayerTip(caller common.Address) (*big.Int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.RelayerBalances == nil {
		g.RelayerBalances = make(map[common.Address]*big.Int)
	}
	amount, exists := g.RelayerBalances[caller]
	if !exists || amount.Sign() <= 0 {
		return nil, fmt.Errorf("no accumulated relayer tip balance to withdraw")
	}
	g.RelayerBalances[caller] = big.NewInt(0)
	return amount, nil
}

// RegisterChainViaStake admits a new chain into ChainRegistry/Governance.ActiveChains WITHOUT a
// committee vote -- a deliberate alternative to ExecuteGovernanceProposal's ProposalRegisterChain
// case, for operators who want registration gated purely by economic stake, not by a
// propose/vote/timelock/execute round from the currently active set (2026-08-28, user request:
// "bỏ cơ chế vote rồi mà sao vẫn còn" -- MinRegistrationStake previously only ADDED a stake
// precondition on top of the existing vote requirement; this is the actual vote-free path).
//
// STAKE MODEL (rewritten 2026-08-28, superseding the PerChainAllocation-based version): this
// function performs NO stake check itself anymore. GatewayEngine has no AccountStateDB access, so
// it cannot verify a real wallet balance -- and PerChainAllocation (the old basis) turned out to
// be the wrong instrument entirely: it is a chain-ID-keyed, governance-vote-only ledger entry
// (moved solely via ProposalTransferAllocation/ProposalAllocateSupply), not something any wallet
// actually holds or can pay with, which does not match "cọc tiền từ ví thật" (a real, liquid
// deposit from an actual wallet, in Root Anchor's own native coin -- explicitly NOT an ERC-20-style
// token) -- the user's explicit design for this path. The real gate now lives one layer up, in
// gateway_handler.go's "registerChainViaStake" case: it requires engine.MinNativeStakeToRegister
// to be configured (>0), checks tx.FromAddress()'s REAL native balance via AccountStateDB against
// it, calls this function, and -- only if that succeeds -- moves the stake out of the caller's
// real wallet into GATEWAY_CONTRACT_ADDRESS as a permanent, held on-chain deposit (burn-then-mint,
// same balance-mutation-last-after-every-checkable-failure ordering as the "outbound" case's
// Value/Tip/GasFee lock). This is also why BootstrapFoundingChains was retired the same day (2026-08-28, see
// note/cross_chain_stake_and_value_flow.md): it processed a BATCH of founding chains from ONE
// coordinator transaction, with no natural per-chain caller wallet to check a real balance
// against -- RegisterChainViaStake (already per-chain) is now the universal registration path for
// every chain, including chain #1, founding or not.
//
// PerChainAllocation as an OUTCOME (2026-09-04, distinct from the above -- that section is about
// the STAKE CHECK, never PerChainAllocation-based): once the real deposit is verified+burned,
// this function credits the SAME amount into the new chain's PerChainAllocation on the Reserve's
// ledger, unifying "stake to register" and "circulating cross-chain allocation" -- see the
// dedicated comment at the bottom of this function's body for the full rationale.
func (g *GatewayEngine) RegisterChainViaStake(payload []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.EnsureGovernance()

	var reg ChainRegistry
	if err := json.Unmarshal(payload, &reg); err != nil {
		return fmt.Errorf("invalid ChainRegistry payload: %w", err)
	}
	if reg.ChainID == 0 {
		return fmt.Errorf("invalid chain ID: 0")
	}
	if _, exists := g.ChainRegistry[reg.ChainID]; exists {
		return fmt.Errorf("%w: chain %d", ErrChainAlreadyRegistered, reg.ChainID)
	}
	// Same PoP bar as ProposalRegisterChain's non-empty-committee case -- an empty committee is
	// still allowed here too (routing-metadata-only registration, deferred to a later
	// ProposalUpdateCommittee), matching the existing pattern.
	if len(reg.Committee) > 0 {
		if err := ValidateCommittee(reg.Committee); err != nil {
			return fmt.Errorf("RegisterChainViaStake: chain %d: %w", reg.ChainID, err)
		}
	}
	if err := ValidateQuorumThreshold(reg.QuorumThreshold); err != nil {
		return fmt.Errorf("RegisterChainViaStake: chain %d: %w", reg.ChainID, err)
	}
	// No stake/balance check here -- see this function's doc comment. The caller
	// (gateway_handler.go's "registerChainViaStake" case) already verified and burned a real
	// native-coin deposit from tx.FromAddress() before invoking this function.

	// Unify "stake to register" and "circulating cross-chain allocation" into one real
	// instrument (2026-09-04 user request: "dùng số tiền cọc và nạp đấy là số tiền để luân
	// chuyển" -- use the staked deposit itself as the money that circulates), instead of
	// requiring a SEPARATE ProposalAllocateSupply/ProposalTransferAllocation step before a
	// freshly-registered chain has any cross-chain-outbound capacity at all (the old "chain
	// nghèo mãi mãi" gap, note/eurozone_unified_native_coin_plan.md mục 2.4).
	//
	// SECURITY FIX (2026-09-04, same day): the very first version of this block called
	// GrantAllocation, which INCREASES GenesisTotalSupply by the credited amount -- a real,
	// unbounded, repeatable, vote-free mint (found on user request to re-check for exactly this).
	// The "real deposit" backing that mint (tx.FromAddress()'s balance on Root Anchor, checked
	// one layer up in gateway_handler.go) is NOT itself provably traceable to GenesisTotalSupply:
	// Root Anchor's own AccountStateDB balances come from its own genesis.json alloc, which is
	// independently, arbitrarily set (gen_root_anchor_chain.py) with zero relation to
	// GenesisTotalSupply. So crediting via GrantAllocation let ANYONE holding ANY Root Anchor
	// wallet balance -- genesis-arbitrary or not -- mint fresh GenesisTotalSupply once per chain
	// registered, unlimited times, no vote, no cap. That is strictly worse than
	// ProposalAllocateSupply's own one-time+Reserve-only mint gate.
	//
	// Fixed by using TransferAllocation instead: it MOVES allocation the Reserve already holds in
	// its own PerChainAllocation[ReserveChainID] pool (itself bounded by the one-time
	// ProposalAllocateSupply mint) to the new chain -- GenesisTotalSupply never changes, "no vote
	// needed" is preserved (TransferAllocation itself has no governance gate, matching
	// RegisterChainViaStake's whole vote-free premise), and the constraint becomes exactly what
	// the user asked for: Reserve must ALREADY hold enough real, previously-minted allocation to
	// cover the amount -- never silently prints more. If Reserve's pool is ever exhausted, no
	// further chain gets funded via this path until the community moves more allocation to
	// Reserve's pool via ProposalTransferAllocation (still vote-gated) -- same "fixed pie,
	// redistributed" principle as every other allocation movement in this ledger (mục 3,
	// note/eurozone_unified_native_coin_plan.md).
	//
	// Only meaningful on the Reserve's own authoritative copy of SupplyLedger -- per note §2.3
	// (and attestCommitInternal's enforceCeiling check), every OTHER chain's local copy of
	// PerChainAllocation has no real enforcement power, so crediting it there would just be
	// confusing, powerless bookkeeping.
	//
	// SECURITY-FIX-INDUCED BOOTSTRAP REGRESSION, found+fixed same day via a real full-pipeline
	// deploy run (run_full_pipeline.sh): an insufficient Reserve pool used to make the WHOLE
	// registration fail closed. That is correct once Reserve already has a pool -- but at genesis
	// of a brand-new system, Reserve's pool starts at exactly 0 (GenesisTotalSupply hasn't been
	// minted yet), and minting it (ProposalAllocateSupply, in register_chains' fundGenesis) itself
	// needs quorum from ALREADY-ACTIVE chains -- which, for the very first chains in a new system,
	// means registering them FIRST. Blocking registration on Reserve's pool made that circular:
	// register needs mint, mint needs registered voters. Fixed by making the credit step
	// best-effort: ErrInsufficientAllocation specifically is swallowed (chain still registers,
	// simply with 0 allocation for now -- exactly the old, working pre-stake-credit behavior, and
	// still recoverable afterward via ExecuteGovernanceProposal's existing ProposalTransferAllocation
	// case, e.g. fundGenesis's own per-chain distribution loop, unchanged). Any OTHER error from
	// TransferAllocation (ErrSameChainTransfer, ErrNilAmount -- real misuse, not a timing issue)
	// still fails the whole registration closed, unchanged from before.
	//
	// Same best-effort treatment applies to GenesisWallet being unset: a zero address here would
	// mean the credit gets aimed at nobody (permanently unspendable, and impossible to later
	// publish a digest for -- SetGenesisDigest requires caller == GenesisWallet), so it's not
	// safe to credit -- but that is still only a reason to SKIP funding, not to refuse the whole
	// registration (in production this never actually happens: gateway_handler.go's
	// "registerChainViaStake" case forces GenesisWallet = tx.FromAddress() before this function
	// ever sees the payload, and a real signed transaction's sender is never the zero address --
	// this only matters for a direct caller, e.g. a test, that sets MinNativeStakeToRegister
	// without also setting GenesisWallet).
	if g.LocalChainID == g.ReserveChainID && g.SupplyLedger != nil &&
		g.MinNativeStakeToRegister != nil && g.MinNativeStakeToRegister.Sign() > 0 &&
		reg.GenesisWallet != (common.Address{}) {
		if err := g.SupplyLedger.TransferAllocation(g.ReserveChainID, reg.ChainID, g.MinNativeStakeToRegister); err != nil {
			if !errors.Is(err, ErrInsufficientAllocation) {
				return fmt.Errorf("RegisterChainViaStake: chain %d: crediting stake into Reserve's allocation pool: %w", reg.ChainID, err)
			}
			// Insufficient pool -- fall through and register anyway, unfunded for now.
		}
	}

	if g.ChainRegistry == nil {
		g.ChainRegistry = make(map[uint64]ChainRegistry)
	}
	g.ChainRegistry[reg.ChainID] = reg
	g.Governance.RegisterActiveChain(reg.ChainID)

	return nil
}

// SetGenesisDigest publishes the canonical genesis.json digest for a chain registered via
// RegisterChainViaStake (2026-09-04, deterministic-genesis design) -- a 2-phase flow, not
// combinable into RegisterChainViaStake itself, because the digest can only be computed AFTER
// genesis.json actually exists: register first (this is what determines GenesisWallet and, via
// PerChainAllocation, the exact initial alloc amount every validator's genesis.json must agree
// on) -- then every validator independently builds genesis.json from that public record and
// SHOULD arrive at byte-identical output -- then the registrant publishes ONE of those (or an
// independently recomputed) digest back here, so any future observer can fetch this digest
// FIRST and verify their own locally-built genesis.json against it before trusting/joining the
// chain, the same "recompute and compare, fail closed on mismatch" defense
// pkg/cross_chain/ceremony.VerifyGenesisFile already provides for the founding-chain ceremony
// path -- this is that same idea, just recorded on-chain instead of in a hand-distributed
// genesis_digest.txt.
//
// Settable exactly once (ErrGenesisDigestAlreadySet on any later call, matching
// ProposalAllocateSupply's own one-time-only pattern) and only by the chain's own recorded
// GenesisWallet (ErrNotGenesisWallet otherwise) -- restricting the caller, not leaving this
// permissionless, specifically closes a front-running race: a permissionless "first digest wins"
// design would let an attacker publish a WRONG digest before the honest registrant gets to it,
// silently locking every honest future validator out of ever passing verification while the
// attacker's own tampered genesis file (built to match their own submitted digest) sails
// through. Requiring GenesisWallet closes that: only the address that actually paid the real
// stake (see RegisterChainViaStake's own doc comment on why GenesisWallet can't be spoofed to a
// third party) can publish, so an attacker without that key cannot front-run at all.
func (g *GatewayEngine) SetGenesisDigest(chainID uint64, digest common.Hash, caller common.Address) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	reg, exists := g.ChainRegistry[chainID]
	if !exists {
		return fmt.Errorf("%w: chain %d", ErrUnknownSourceChain, chainID)
	}
	if reg.GenesisDigest != (common.Hash{}) {
		return fmt.Errorf("%w: chain %d", ErrGenesisDigestAlreadySet, chainID)
	}
	if reg.GenesisWallet == (common.Address{}) || caller != reg.GenesisWallet {
		return fmt.Errorf("%w: chain %d", ErrNotGenesisWallet, chainID)
	}
	if digest == (common.Hash{}) {
		return fmt.Errorf("SetGenesisDigest: chain %d: digest must be non-zero", chainID)
	}

	reg.GenesisDigest = digest
	g.ChainRegistry[chainID] = reg
	return nil
}

// ExecuteGovernanceProposal executes an approved governance proposal after the timelock and
// mutates GatewayEngine state (ChainRegistry onboarding/offboarding, dead chains, asset registration).
func (g *GatewayEngine) ExecuteGovernanceProposal(proposalID common.Hash, currentTimestamp uint64) (*GovernanceProposal, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.EnsureGovernance()

	proposal, err := g.Governance.Execute(proposalID, currentTimestamp)
	if err != nil {
		return nil, err
	}

	switch proposal.Kind {
	case ProposalRegisterChain:
		var reg ChainRegistry
		if err := json.Unmarshal(proposal.Payload, &reg); err != nil {
			return nil, fmt.Errorf("invalid ChainRegistry payload: %w", err)
		}
		if reg.ChainID == 0 {
			return nil, fmt.Errorf("invalid chain ID: 0")
		}
		// Security fix: a NON-EMPTY committee here was previously accepted with no PoP
		// verification at all — unlike BootstrapFoundingChains and ProposalUpdateCommittee,
		// which both require it — letting a proposal register a chain whose "committee" is
		// unverifiable rogue keys. An EMPTY committee is still allowed (registers routing
		// metadata only; VerifyQuorumCertAgainstRegistry fails closed with ErrEmptyCommittee
		// for any quorum cert against it until a real committee is set via
		// ProposalUpdateCommittee) — this matches an existing real usage pattern
		// (gateway_governance_test.go's asset-registration test onboards a chain this way)
		// that a blanket non-empty requirement would have broken.
		if len(reg.Committee) > 0 {
			if err := ValidateCommittee(reg.Committee); err != nil {
				return nil, fmt.Errorf("ProposalRegisterChain: chain %d: %w", reg.ChainID, err)
			}
		}
		if err := ValidateQuorumThreshold(reg.QuorumThreshold); err != nil {
			return nil, fmt.Errorf("ProposalRegisterChain: chain %d: %w", reg.ChainID, err)
		}
		// C6 mitigation (opt-in, see MinRegistrationStake's doc comment): require the candidate
		// chain ID to already hold a minimum pre-funded allocation before it can be admitted.
		if g.MinRegistrationStake != nil && g.MinRegistrationStake.Sign() > 0 {
			held := new(big.Int)
			if g.SupplyLedger != nil {
				held = g.SupplyLedger.GetAllocation(reg.ChainID)
			}
			if held.Cmp(g.MinRegistrationStake) < 0 {
				return nil, fmt.Errorf("%w: chain %d holds %s, needs >= %s", ErrInsufficientRegistrationStake, reg.ChainID, held.String(), g.MinRegistrationStake.String())
			}
		}
		g.Governance.RegisterActiveChain(reg.ChainID)
		if g.ChainRegistry == nil {
			g.ChainRegistry = make(map[uint64]ChainRegistry)
		}
		g.ChainRegistry[reg.ChainID] = reg

	case ProposalUnregisterChain:
		var chainID uint64
		if len(proposal.Payload) == 8 {
			chainID = binary.BigEndian.Uint64(proposal.Payload)
		} else if err := json.Unmarshal(proposal.Payload, &chainID); err != nil {
			return nil, fmt.Errorf("invalid unregister chain ID payload: %w", err)
		}
		g.Governance.UnregisterActiveChain(chainID)
		delete(g.ChainRegistry, chainID)

	case ProposalDeclareChainDead:
		var chainID uint64
		if len(proposal.Payload) == 8 {
			chainID = binary.BigEndian.Uint64(proposal.Payload)
		} else if err := json.Unmarshal(proposal.Payload, &chainID); err != nil {
			return nil, fmt.Errorf("invalid declare dead chain ID payload: %w", err)
		}
		if g.DeadChains == nil {
			g.DeadChains = make(map[uint64]bool)
		}
		g.DeadChains[chainID] = true

	case ProposalAllocateSupply:
		// C7 fix (2026-08-27): this used to accept ANY chainID, repeatably, effectively minting
		// fresh GenesisTotalSupply via nothing but a governance vote every time -- a real
		// Sybil-mintable path distinct from ClaimMessage's safe transfer-based auto-credit (see
		// note/cross_chain_attack_scenario_catalog.md item C7 and
		// note/eurozone_unified_native_coin_plan.md). Restricted to what the design doc's own
		// mục 2.1/2.3 actually specifies: genesis_total_supply is minted EXACTLY ONCE, entirely
		// TO Reserve -- this is now that one-time act, not a repeatable grant to arbitrary
		// chains. Every chain other than Reserve must earn allocation the safe way: receive a
		// real transfer via outbound()/ClaimMessage (already-existing, already-tested,
		// non-inflationary), or via GlobalSupplyLedger.TransferAllocation moving Reserve's own
		// already-minted supply outward (existing primitive, was never wired to any proposal
		// kind before this fix -- see ProposalTransferAllocation below).
		var grant AllocationGrantPayload
		if err := json.Unmarshal(proposal.Payload, &grant); err != nil {
			return nil, fmt.Errorf("invalid AllocationGrantPayload: %w", err)
		}
		if grant.ChainID == 0 {
			return nil, fmt.Errorf("invalid chain ID: 0")
		}
		if g.SupplyLedger == nil {
			return nil, fmt.Errorf("ProposalAllocateSupply: SupplyLedger not initialized")
		}
		if g.ReserveChainID == 0 {
			return nil, fmt.Errorf("ProposalAllocateSupply: %w", ErrReserveChainNotConfigured)
		}
		if grant.ChainID != g.ReserveChainID {
			return nil, fmt.Errorf("ProposalAllocateSupply: %w (got chain %d, reserve is %d)", ErrOnlyReserveMayMint, grant.ChainID, g.ReserveChainID)
		}
		if g.SupplyLedger.GenesisTotalSupply != nil && g.SupplyLedger.GenesisTotalSupply.Sign() > 0 {
			return nil, fmt.Errorf("ProposalAllocateSupply: %w", ErrGenesisAlreadyMinted)
		}
		if err := g.SupplyLedger.GrantAllocation(grant.ChainID, grant.Amount); err != nil {
			return nil, fmt.Errorf("ProposalAllocateSupply: %w", err)
		}

	case ProposalTransferAllocation:
		// C7 fix (2026-08-27): the safe, repeatable, non-inflationary path for a chain to gain
		// allocation after genesis -- redistributes already-minted supply (TransferAllocation
		// itself enforces FromChainID actually has the amount; it can never create new supply,
		// only move existing supply, so no ceiling/Reserve restriction is needed here the way
		// ProposalAllocateSupply needed one).
		var transfer AllocationTransferPayload
		if err := json.Unmarshal(proposal.Payload, &transfer); err != nil {
			return nil, fmt.Errorf("invalid AllocationTransferPayload: %w", err)
		}
		if transfer.FromChainID == 0 || transfer.ToChainID == 0 {
			return nil, fmt.Errorf("invalid chain ID: 0")
		}
		if g.SupplyLedger == nil {
			return nil, fmt.Errorf("ProposalTransferAllocation: SupplyLedger not initialized")
		}
		if err := g.SupplyLedger.TransferAllocation(transfer.FromChainID, transfer.ToChainID, transfer.Amount); err != nil {
			return nil, fmt.Errorf("ProposalTransferAllocation: %w", err)
		}

	case ProposalUpdateCommittee:
		var update UpdateCommitteePayload
		if err := json.Unmarshal(proposal.Payload, &update); err != nil {
			return nil, fmt.Errorf("invalid UpdateCommitteePayload: %w", err)
		}
		if update.ChainID == 0 && update.SourceChainID != 0 {
			update.ChainID = update.SourceChainID
		}
		if update.ChainID == 0 {
			return nil, fmt.Errorf("invalid chain ID: 0")
		}
		reg, exists := g.ChainRegistry[update.ChainID]
		if !exists {
			return nil, fmt.Errorf("%w: chain %d", ErrUnknownChain, update.ChainID)
		}
		if err := ValidateCommittee(update.NewCommittee); err != nil {
			return nil, fmt.Errorf("ProposalUpdateCommittee: %w", err)
		}
		// Security fix: QuorumThreshold was applied with no bounds check — a governance
		// proposal (even an honestly-intended one with a typo) could set it below the 2/3 BFT
		// floor, letting a minority of this chain's new committee forge a "valid" QuorumCert
		// for every future attestCommit()/vote() against it.
		if err := ValidateQuorumThreshold(update.QuorumThreshold); err != nil {
			return nil, fmt.Errorf("ProposalUpdateCommittee: chain %d: %w", update.ChainID, err)
		}
		reg.Committee = update.NewCommittee
		if update.NewEpoch > 0 {
			reg.Epoch = update.NewEpoch
		}
		if update.QuorumThreshold > 0 {
			reg.QuorumThreshold = update.QuorumThreshold
		}
		if update.StateRoot != (common.Hash{}) {
			reg.StateRoot = update.StateRoot
		}
		if update.AccountTreeRoot != (common.Hash{}) {
			reg.AccountTreeRoot = update.AccountTreeRoot
		}
		g.ChainRegistry[update.ChainID] = reg
	}

	return proposal, nil
}

// SetAllocationRejectedListener registers an instant alert listener for overdraw events.
func (g *GatewayEngine) SetAllocationRejectedListener(l AllocationRejectedListener) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.allocationRejectedListener = l
}

func Keccak256(data []byte) common.Hash {
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(data)
	var out common.Hash
	hasher.Sum(out[:0])
	return out
}

// hashPair combines two Merkle tree hashes with RFC 6962 domain separation (0x01 for internal nodes).
func hashPair(a, b common.Hash) common.Hash {
	var combined []byte
	combined = append(combined, 0x01) // Domain separation: 0x01 for internal node
	if bytes.Compare(a.Bytes(), b.Bytes()) <= 0 {
		combined = append(combined, a.Bytes()...)
		combined = append(combined, b.Bytes()...)
	} else {
		combined = append(combined, b.Bytes()...)
		combined = append(combined, a.Bytes()...)
	}
	return Keccak256(combined)
}

// VerifyMerkleProof verifies Merkle membership using domain-separated pair hashing.
func VerifyMerkleProof(leaf common.Hash, proof MerkleProof, expectedRoot common.Hash) bool {
	current := leaf
	for _, sibling := range proof.Siblings {
		current = hashPair(current, sibling)
	}
	return current == expectedRoot
}

// CanonicalEncodeMessage serializes a CrossChainMessage into canonical deterministic binary representation (Section 11.2).
// Fixed-width formatting for all numeric and hash fields with length prefix for variable payload ensures zero boundary ambiguity.
func CanonicalEncodeMessage(m CrossChainMessage) []byte {
	var buf []byte
	buf = append(buf, m.MessageID.Bytes()...)
	var cBuf [8]byte
	binary.BigEndian.PutUint64(cBuf[:], m.SourceChainID)
	buf = append(buf, cBuf[:]...)
	binary.BigEndian.PutUint64(cBuf[:], m.DestChainID)
	buf = append(buf, cBuf[:]...)
	binary.BigEndian.PutUint64(cBuf[:], m.Sequence)
	buf = append(buf, cBuf[:]...)
	buf = append(buf, m.Sender.Bytes()...)
	buf = append(buf, m.Target.Bytes()...)

	// Fixed 32 bytes for AssetID (uint256 big-endian)
	assetBytes := make([]byte, 32)
	if m.AssetID != nil {
		raw := m.AssetID.Bytes()
		if len(raw) <= 32 {
			copy(assetBytes[32-len(raw):], raw)
		}
	}
	buf = append(buf, assetBytes...)

	buf = append(buf, byte(m.HopCount))
	if m.Ordered {
		buf = append(buf, 0x01)
	} else {
		buf = append(buf, 0x00)
	}

	// Fixed 32 bytes for Value (uint256 big-endian)
	valBytes := make([]byte, 32)
	if m.Value != nil {
		raw := m.Value.Bytes()
		if len(raw) <= 32 {
			copy(valBytes[32-len(raw):], raw)
		}
	}
	buf = append(buf, valBytes...)

	// Fixed 32 bytes for Tip (uint256 big-endian)
	tipBytes := make([]byte, 32)
	if m.Tip != nil {
		raw := m.Tip.Bytes()
		if len(raw) <= 32 {
			copy(tipBytes[32-len(raw):], raw)
		}
	}
	buf = append(buf, tipBytes...)

	// Fixed 32 bytes for GasFee (uint256 big-endian) -- included in the hash so a relayer can't
	// alter the locked cross-chain gas budget in transit (same integrity requirement as every
	// other economic field here).
	gasFeeBytes := make([]byte, 32)
	if m.GasFee != nil {
		raw := m.GasFee.Bytes()
		if len(raw) <= 32 {
			copy(gasFeeBytes[32-len(raw):], raw)
		}
	}
	buf = append(buf, gasFeeBytes...)

	// 4-byte length prefix for Payload followed by payload bytes
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(m.Payload)))
	buf = append(buf, lenBuf[:]...)
	buf = append(buf, m.Payload...)
	return buf
}

// ComputeMessageLeafHash computes the canonical leaf hash with 0x00 domain separation prefix.
func ComputeMessageLeafHash(m CrossChainMessage) common.Hash {
	data := append([]byte{0x00}, CanonicalEncodeMessage(m)...)
	return Keccak256(data)
}

// Outbound handles initiating a cross-chain message on the source chain (P2.1 & P2.5).
func (g *GatewayEngine) Outbound(
	sender common.Address,
	params OutboundParams,
	txHash common.Hash,
) (*CrossChainMessage, error) {
	if params.HopCount > MaxHopCount {
		return nil, fmt.Errorf("%w: got %d, max allowed %d", ErrHopCountExceeded, params.HopCount, MaxHopCount)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	messageID := txHash
	g.MessageStatus[messageID] = MessageStatusPending

	if params.Tip != nil && params.Tip.Sign() > 0 {
		g.LockedTips[messageID] = new(big.Int).Set(params.Tip)
	}

	seqKey := fmt.Sprintf("%d:%d", g.LocalChainID, params.DestChainID)
	seq := g.ChannelSequence[seqKey] + 1
	g.ChannelSequence[seqKey] = seq

	val := big.NewInt(0)
	if params.Value != nil {
		val = new(big.Int).Set(params.Value)
	}
	tip := big.NewInt(0)
	if params.Tip != nil {
		tip = new(big.Int).Set(params.Tip)
	}
	gasFee := big.NewInt(0)
	if params.GasFee != nil {
		gasFee = new(big.Int).Set(params.GasFee)
	}
	assetID := big.NewInt(0)
	if params.AssetID != nil {
		assetID = new(big.Int).Set(params.AssetID)
	}

	msg := &CrossChainMessage{
		MessageID:     messageID,
		SourceChainID: g.LocalChainID,
		DestChainID:   params.DestChainID,
		Sender:        sender,
		Target:        params.Target,
		Payload:       params.Payload,
		AssetID:       assetID,
		Value:         val,
		Sequence:      seq,
		Tip:           tip,
		GasFee:        gasFee,
		HopCount:      params.HopCount,
		Ordered:       params.Ordered,
	}

	if g.PendingOutboundMessages == nil {
		g.PendingOutboundMessages = make(map[uint64][]CrossChainMessage)
	}
	g.PendingOutboundMessages[params.DestChainID] = append(g.PendingOutboundMessages[params.DestChainID], *msg)

	return msg, nil
}

// AddPendingCommitteeAttestationShare thread-safely adds a committee attestation share.
func (g *GatewayEngine) AddPendingCommitteeAttestationShare(key string, share CommitteeAttestationShare) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, s := range g.PendingCommitteeAttestations[key] {
		if bytes.Equal(s.SignerPubkeyBLS, share.SignerPubkeyBLS) {
			return fmt.Errorf("pubkey already submitted a share")
		}
	}
	g.PendingCommitteeAttestations[key] = append(g.PendingCommitteeAttestations[key], share)
	return nil
}

// GetPendingCommitteeAttestationShares thread-safely reads committee attestation shares.
func (g *GatewayEngine) GetPendingCommitteeAttestationShares(key string) []CommitteeAttestationShare {
	g.mu.RLock()
	defer g.mu.RUnlock()
	shares := g.PendingCommitteeAttestations[key]
	res := make([]CommitteeAttestationShare, len(shares))
	copy(res, shares)
	return res
}

// ClearPendingCommitteeAttestations thread-safely clears committee attestation shares.
func (g *GatewayEngine) ClearPendingCommitteeAttestations(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.PendingCommitteeAttestations, key)
}

// AddPendingCommitAttestationShare thread-safely adds a commit attestation share.
func (g *GatewayEngine) AddPendingCommitAttestationShare(key string, share CommitAttestationShare) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, s := range g.PendingCommitAttestations[key] {
		if bytes.Equal(s.SignerPubkeyBLS, share.SignerPubkeyBLS) {
			return fmt.Errorf("pubkey already submitted a share")
		}
	}
	g.PendingCommitAttestations[key] = append(g.PendingCommitAttestations[key], share)
	return nil
}

// GetPendingCommitAttestationShares thread-safely reads commit attestation shares.
func (g *GatewayEngine) GetPendingCommitAttestationShares(key string) []CommitAttestationShare {
	g.mu.RLock()
	defer g.mu.RUnlock()
	shares := g.PendingCommitAttestations[key]
	res := make([]CommitAttestationShare, len(shares))
	copy(res, shares)
	return res
}

// CommittedOutboundBatch is the message set (and signing epoch) behind one commitRoot ever
// produced by BatchOutboundCommit — see PendingOutboundMessages/CommittedBatches' doc comments.
type CommittedOutboundBatch struct {
	Messages []CrossChainMessage `json:"messages"`
	Epoch    uint64              `json:"epoch"`
}

// BatchOutboundCommit takes every currently-pending outbound() message queued for destChainID on
// this chain, builds a real commit tree (BuildCommitTree), and returns its root -- the same root
// this chain's own committee must now BLS-attest (via CommitAttestationWorker.OnCommitFinalized,
// wired from gateway_handler.go's batchOutboundCommit() case) before any relayer can submit
// attestCommit()/claimMessage() against it on destChainID. Permissionless like committeeUpdate()
// (anyone may call it, redundant/early calls are harmless) -- every message it ever batches
// already passed its own outbound() validation and had its sender's funds burned/locked for
// real, so there is nothing to grief by calling this early, often, or as a non-participant.
func (g *GatewayEngine) BatchOutboundCommit(destChainID uint64, epoch uint64) (common.Hash, []CrossChainMessage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	messages := g.PendingOutboundMessages[destChainID]
	if len(messages) == 0 {
		return common.Hash{}, nil, fmt.Errorf("no pending outbound messages for destination chain %d", destChainID)
	}

	commitRoot, _, _, _, err := BuildCommitTree(messages)
	if err != nil {
		return common.Hash{}, nil, fmt.Errorf("build commit tree: %w", err)
	}

	if g.CommittedBatches == nil {
		g.CommittedBatches = make(map[common.Hash]CommittedOutboundBatch)
	}
	g.CommittedBatches[commitRoot] = CommittedOutboundBatch{Messages: messages, Epoch: epoch}
	delete(g.PendingOutboundMessages, destChainID)

	return commitRoot, messages, nil
}

// GetPendingOutboundCount thread-safely reads the length of PendingOutboundMessages for a destChainID.
func (g *GatewayEngine) GetPendingOutboundCount(destChainID uint64) int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.PendingOutboundMessages[destChainID])
}

// GetCommittedBatch thread-safely reads a CommittedOutboundBatch.
func (g *GatewayEngine) GetCommittedBatch(commitRoot common.Hash) (CommittedOutboundBatch, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	batch, exists := g.CommittedBatches[commitRoot]
	return batch, exists
}

// AttestCommit executes Phase 1 of Attest-then-Claim (P2.2) for a commit originating from a
// registered PRIVATE CHAIN. It enforces and debits per_chain_allocation[sourceChainID] — this is
// the ONLY leg of a transfer where a ceiling is meaningful (Section 2.3 step 2). The matching
// credit to the final destination chain happens in ClaimMessage (Section 2.3.1 fix), not here,
// because a single commit can route to multiple distinct destinations.
func (g *GatewayEngine) AttestCommit(
	sourceChainID uint64,
	commitRoot common.Hash,
	aggregateAmount *big.Int,
	assetId *big.Int,
	aggregateProof MerkleProof,
	cert QuorumCert,
) (*AttestedCommit, error) {
	return g.attestCommitInternal(sourceChainID, commitRoot, aggregateAmount, assetId, aggregateProof, cert, true)
}

// AttestReserveIssuedCommit executes Phase 1 of Attest-then-Claim for a commit issued by RESERVE
// itself (the Reserve->destination leg, Section 2.3 step 3). Reserve is the unconditional issuer:
// there is no per_chain_allocation ceiling to enforce against Reserve's own entry, and this call
// MUST NOT debit it — doing so was the root cause of the "double debit, zero credit" supply
// invariant violation found during review (100 transferred -> 200 destroyed from the ledger).
// Full BLS quorum verification still applies; only the ceiling/debit step is skipped.
func (g *GatewayEngine) AttestReserveIssuedCommit(
	reserveChainID uint64,
	commitRoot common.Hash,
	aggregateAmount *big.Int,
	assetId *big.Int,
	aggregateProof MerkleProof,
	cert QuorumCert,
) (*AttestedCommit, error) {
	return g.attestCommitInternal(reserveChainID, commitRoot, aggregateAmount, assetId, aggregateProof, cert, false)
}

func (g *GatewayEngine) attestCommitInternal(
	sourceChainID uint64,
	commitRoot common.Hash,
	aggregateAmount *big.Int,
	assetId *big.Int,
	aggregateProof MerkleProof,
	cert QuorumCert,
	enforceCeiling bool,
) (*AttestedCommit, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	registry, exists := g.ChainRegistry[sourceChainID]
	if !exists {
		return nil, fmt.Errorf("%w: chain %d", ErrUnknownSourceChain, sourceChainID)
	}

	// Fail-closed epoch verification
	if cert.Epoch != registry.Epoch {
		return nil, fmt.Errorf("%w: expected %d, got %d", ErrEpochMismatch, registry.Epoch, cert.Epoch)
	}

	// Fail-closed: Committee cannot be empty
	commitMsg := ComputeCommitRootAttestMessage(commitRoot)
	if err := VerifyQuorumCertAgainstRegistry(registry, cert, commitMsg); err != nil {
		return nil, err
	}

	if aggregateAmount == nil {
		aggregateAmount = big.NewInt(0)
	}
	if assetId == nil {
		assetId = big.NewInt(0)
	}

	// Verify cryptographic binding of the declared aggregateAmount to the commit itself (Section
	// 2.3.1/11.2, risk #20): aggregateAmount must be provably a leaf of the SAME Merkle tree whose
	// root is commitRoot — the very thing cert (verified above) already BLS-attests to — not a
	// number the caller simply asserts. Verifying against commitRoot (not registry.StateRoot) is
	// deliberate: it's what BuildCommitTree actually embeds the leaf into (relayer.go), and it's
	// what makes AttestReserveIssuedCommit's re-attestation of the same underlying commit on the
	// Reserve->destination leg check out identically, since both legs share the same commitRoot.
	leaf := AggregateValueLeaf{
		AssetID:         assetId,
		AggregateAmount: aggregateAmount,
	}
	leafHash := HashAggregateValueLeaf(leaf)
	if !VerifyMerkleProof(leafHash, aggregateProof, commitRoot) {
		return nil, ErrInvalidMerkleProof
	}

	key := fmt.Sprintf("%d:%s:%s", sourceChainID, commitRoot.Hex(), assetId.String())

	// Enforce write-once semantics
	if existing, exists := g.AttestedCommits[key]; exists {
		if existing.FundedAmount.Cmp(aggregateAmount) != 0 {
			return nil, fmt.Errorf("attestCommit: aggregateAmount mismatch for already-attested asset")
		}
		return &existing, nil
	}

	if enforceCeiling && aggregateAmount.Sign() > 0 {
		// C8 fix (2026-08-27): only the configured Reserve chain may perform a ceiling-enforced
		// attestation of a nonzero-value commit. Before this check, GlobalSupplyLedger being
		// per-chain-LOCAL state meant ANY chain could independently call this same AttestCommit
		// for ANY other chain's commit, decrementing only its own local copy of that source's
		// allocation with zero cross-chain synchronization -- "route value through Reserve" was
		// enforced only by relayer.go's own convention, never by the contract itself. A
		// zero-value commit (message type (a), Section 2.2 -- pure contract calls) is exempt:
		// it never touches the ledger below regardless (Sub(x, 0) is a no-op), so direct A->B
		// messaging without Reserve stays fully intact.
		if g.ReserveChainID == 0 {
			return nil, ErrReserveChainNotConfigured
		}
		if g.LocalChainID != g.ReserveChainID {
			return nil, fmt.Errorf("%w: this chain (%d) is not the configured Reserve (%d)", ErrNonReserveCeilingAttestation, g.LocalChainID, g.ReserveChainID)
		}
	}

	if enforceCeiling {
		// Check per_chain_allocation ceiling (Scenario 10.7) — only meaningful for a private
		// chain's own commit (the X -> Reserve leg). Reserve-issued commits skip this entirely.
		currentAlloc := g.SupplyLedger.GetAllocation(sourceChainID)
		if aggregateAmount.Cmp(currentAlloc) > 0 {
			if g.allocationRejectedListener != nil {
				g.allocationRejectedListener(sourceChainID, aggregateAmount, currentAlloc)
			}
			return nil, fmt.Errorf("%w: requested %s exceeds available %s", ErrAllocationExceeded, aggregateAmount.String(), currentAlloc.String())
		}

		// Debit source chain allocation upon successful BFT attestation. The matching credit to
		// the destination happens per-message in ClaimMessage (Section 2.3.1).
		g.SupplyLedger.PerChainAllocation[sourceChainID] = new(big.Int).Sub(currentAlloc, aggregateAmount)
	}

	attested := AttestedCommit{
		SourceChainID: sourceChainID,
		CommitRoot:    commitRoot,
		AssetID:       new(big.Int).Set(assetId),
		Epoch:         cert.Epoch,
		FundedAmount:  new(big.Int).Set(aggregateAmount),
		ClaimedAmount: big.NewInt(0),
	}
	g.AttestedCommits[key] = attested

	return &attested, nil
}

// ClaimMessage executes Phase 2 of Attest-then-Claim (P2.3 & P2.6).
// Enforces hard-cap on FundedAmount, verifies domain-separated Merkle proof, guards against double-claim, sets execution context.
func (g *GatewayEngine) ClaimMessage(
	message CrossChainMessage,
	proof MerkleProof,
	commitRoot common.Hash,
	relayer common.Address,
) (MessageStatus, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	currentStatus, hasStatus := g.MessageStatus[message.MessageID]
	if hasStatus && currentStatus != MessageStatusPending {
		return currentStatus, fmt.Errorf("%w: message %s has status %d", ErrAlreadyClaimed, message.MessageID.Hex(), currentStatus)
	}

	assetIdStr := "0"
	if message.AssetID != nil {
		assetIdStr = message.AssetID.String()
	}
	key := fmt.Sprintf("%d:%s:%s", message.SourceChainID, commitRoot.Hex(), assetIdStr)
	attested, exists := g.AttestedCommits[key]
	if !exists {
		// 2-hop routed transfers via Reserve / Hub Chain (Section 2.2(b))
		for chainID := range g.ChainRegistry {
			k := fmt.Sprintf("%d:%s:%s", chainID, commitRoot.Hex(), assetIdStr)
			if a, ok := g.AttestedCommits[k]; ok {
				attested = a
				exists = true
				key = k
				break
			}
		}
	}
	if !exists {
		return MessageStatusPending, fmt.Errorf("%w: commit %s on chain %d", ErrCommitNotAttested, commitRoot.Hex(), message.SourceChainID)
	}

	// Hard-cap verification: ClaimedAmount + Value <= FundedAmount (Section 2.3.1)
	if message.Value != nil && message.Value.Sign() > 0 {
		newClaimed := new(big.Int).Add(attested.ClaimedAmount, message.Value)
		if newClaimed.Cmp(attested.FundedAmount) > 0 {
			return MessageStatusPending, fmt.Errorf("%w: commit cap %s exceeded by %s", ErrAllocationExceeded, attested.FundedAmount.String(), newClaimed.String())
		}
		attested.ClaimedAmount = newClaimed
		g.AttestedCommits[key] = attested
	}

	// Canonical leaf hash with 0x00 domain separation
	leafHash := ComputeMessageLeafHash(message)

	if !VerifyMerkleProof(leafHash, proof, commitRoot) {
		return MessageStatusPending, ErrInvalidMerkleProof
	}

	// Credit this chain's allocation ceiling with the value being finalized here (Section 2.3.1
	// fix). This is the missing counterpart to AttestCommit's debit — without it, value silently
	// evaporates from Σ per_chain_allocation on every successful transfer (proven via reproduction:
	// 100 transferred -> 200 destroyed). ClaimMessage is the correct place because it is the only
	// point that knows the message's real, individual DestChainID (a single attested commit can
	// route to several distinct destinations).
	if message.Value != nil && message.Value.Sign() > 0 && g.SupplyLedger != nil {
		if message.DestChainID != g.LocalChainID {
			return MessageStatusPending, fmt.Errorf("%w: message destChainId %d does not match claiming engine's chain %d", ErrInvalidMerkleProof, message.DestChainID, g.LocalChainID)
		}
		currentAlloc := g.SupplyLedger.GetAllocation(g.LocalChainID)
		g.SupplyLedger.PerChainAllocation[g.LocalChainID] = new(big.Int).Add(currentAlloc, message.Value)
	}

	// Set execution context for destination target contracts
	g.ActiveContext = &CrossChainContext{
		OriginalSender: message.Sender,
		SourceChainID:  message.SourceChainID,
		IsGateway:      true,
	}

	// Execution succeeds
	execStatus := MessageStatusSuccess

	// Clear execution context
	g.ActiveContext = nil

	g.MessageStatus[message.MessageID] = execStatus

	// Disburse tip to relayer (P2.3 & P4.2)
	if message.Tip != nil && message.Tip.Sign() > 0 {
		currBal := g.RelayerBalances[relayer]
		if currBal == nil {
			currBal = big.NewInt(0)
		}
		g.RelayerBalances[relayer] = new(big.Int).Add(currBal, message.Tip)
	}

	return execStatus, nil
}

// VerifyQuorumCertAgainstRegistry validates the BLS signature and threshold stake against a chain's committee.
func VerifyQuorumCertAgainstRegistry(registry ChainRegistry, cert QuorumCert, digest []byte) error {
	if len(registry.Committee) == 0 {
		return fmt.Errorf("%w: chain %d", ErrEmptyCommittee, registry.ChainID)
	}
	if len(cert.AggregateSignature) == 0 {
		return ErrInvalidBLSSignature
	}

	var accumulatedStake uint64
	var totalStake uint64
	var votingPubkeys [][]byte

	for i, val := range registry.Committee {
		totalStake += val.Stake
		isSigner := false
		if len(cert.SignerBitmap) > 0 {
			byteIdx := i / 8
			bitIdx := uint(i % 8)
			if byteIdx < len(cert.SignerBitmap) && (cert.SignerBitmap[byteIdx]&(1<<bitIdx)) != 0 {
				isSigner = true
			}
		} else if len(registry.Committee) == 1 {
			// Single validator backward compatibility when bitmap is omitted
			isSigner = true
		}

		if isSigner {
			accumulatedStake += val.Stake
			votingPubkeys = append(votingPubkeys, val.PubkeyBLS)
		}
	}

	if totalStake == 0 {
		return ErrZeroTotalStake
	}

	threshold := (totalStake*2 + 2) / 3
	if registry.QuorumThreshold > 0 {
		threshold = (totalStake*uint64(registry.QuorumThreshold) + 9999) / 10000
	}

	if accumulatedStake < threshold || len(votingPubkeys) == 0 {
		return fmt.Errorf("%w: accumulated stake %d < threshold %d", ErrQuorumNotReached, accumulatedStake, threshold)
	}

	if len(votingPubkeys) == 1 {
		pubKey := cm.PubkeyFromBytes(votingPubkeys[0])
		sig := cm.SignFromBytes(cert.AggregateSignature)
		if !bls.VerifySign(pubKey, sig, digest) {
			return ErrInvalidBLSSignature
		}
	} else {
		msgs := make([][]byte, len(votingPubkeys))
		for i := range msgs {
			msgs[i] = digest
		}
		if !bls.VerifyAggregateSign(votingPubkeys, cert.AggregateSignature, msgs) {
			return ErrInvalidBLSSignature
		}
	}
	return nil
}

// Refund processes returning funds to the sender on the source chain when the destination reverts (P2.4).
func (g *GatewayEngine) Refund(
	message CrossChainMessage,
	messageProof MerkleProof,
	commitRoot common.Hash,
	destFailureCert QuorumCert,
) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 1. Refund must be processed on the message's source chain (where value was burned)
	if message.SourceChainID != g.LocalChainID {
		return fmt.Errorf("refund must be processed on source chain %d, got local chain %d", message.SourceChainID, g.LocalChainID)
	}

	// 2. Message status must be Pending (never resolved before)
	status, exists := g.MessageStatus[message.MessageID]
	if !exists {
		status = MessageStatusPending
	}
	if status != MessageStatusPending {
		return fmt.Errorf("%w: message %s current status is %d", ErrInvalidRefundState, message.MessageID.Hex(), status)
	}

	// 3. Verify message was part of an attested commit on this source chain
	key := fmt.Sprintf("%d:%s:%s", message.SourceChainID, commitRoot.Hex(), message.AssetID.String())
	_, exists = g.AttestedCommits[key]
	if !exists {
		for _, v := range g.AttestedCommits {
			if v.SourceChainID == message.SourceChainID && v.CommitRoot == commitRoot {
				exists = true
				break
			}
		}
	}
	if !exists {
		return fmt.Errorf("%w: commit %s on chain %d", ErrCommitNotAttested, commitRoot.Hex(), message.SourceChainID)
	}

	// 4. Verify message Merkle proof against commitRoot
	leafHash := ComputeMessageLeafHash(message)
	if !VerifyMerkleProof(leafHash, messageProof, commitRoot) {
		return ErrInvalidMerkleProof
	}

	// 5. Verify destination failure QuorumCert
	destRegistry, hasDest := g.ChainRegistry[message.DestChainID]
	if !hasDest {
		return fmt.Errorf("%w: destination chain %d", ErrUnknownSourceChain, message.DestChainID)
	}
	if destFailureCert.Epoch != destRegistry.Epoch {
		return fmt.Errorf("%w: expected epoch %d, got %d", ErrEpochMismatch, destRegistry.Epoch, destFailureCert.Epoch)
	}

	failureDigest := ComputeMessageFailureAttestMessage(message.MessageID, message.DestChainID)
	if err := VerifyQuorumCertAgainstRegistry(destRegistry, destFailureCert, failureDigest); err != nil {
		return fmt.Errorf("%w: destination failure cert verification failed: %v", ErrInvalidRefundProof, err)
	}

	// 6. Atomically set status to Refunded
	g.MessageStatus[message.MessageID] = MessageStatusRefunded

	// 7. Restore allocation in GlobalSupplyLedger
	if g.SupplyLedger != nil && message.Value != nil && message.Value.Sign() > 0 {
		currentAlloc := g.SupplyLedger.GetAllocation(message.SourceChainID)
		g.SupplyLedger.PerChainAllocation[message.SourceChainID] = new(big.Int).Add(currentAlloc, message.Value)
	}

	return nil
}

// VerifyAndExecute handles atomic verification & execution for low-volume messages (P2.7).
func (g *GatewayEngine) VerifyAndExecute(
	message CrossChainMessage,
	aggregateProof MerkleProof,
	cert QuorumCert,
	messageProof MerkleProof,
	commitRoot common.Hash,
	relayer common.Address,
) (MessageStatus, error) {
	if _, err := g.AttestCommit(message.SourceChainID, commitRoot, message.Value, message.AssetID, aggregateProof, cert); err != nil {
		return MessageStatusPending, err
	}
	return g.ClaimMessage(message, messageProof, commitRoot, relayer)
}

// ClaimDeadChainBalance allows user to recover funds on Reserve using account-tree Merkle proof (P2.8).
func (g *GatewayEngine) ClaimDeadChainBalance(
	deadChainID uint64,
	account common.Address,
	amount *big.Int,
	proof MerkleProof,
	accountLeafHash common.Hash,
) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.DeadChains[deadChainID] {
		return fmt.Errorf("%w: chain %d", ErrChainNotDead, deadChainID)
	}

	claimKey := fmt.Sprintf("%d:%s", deadChainID, account.Hex())
	if g.DeadChainClaimed[claimKey] {
		return fmt.Errorf("%w: chain %d, account %s", ErrDeadChainAlreadyClaimed, deadChainID, account.Hex())
	}

	expectedLeafHash := HashAccountLeaf(AccountLeaf{Account: account, Balance: amount})
	if accountLeafHash != expectedLeafHash {
		return fmt.Errorf("accountLeafHash %s does not match computed %s", accountLeafHash.Hex(), expectedLeafHash.Hex())
	}

	registry, exists := g.ChainRegistry[deadChainID]
	if !exists {
		return fmt.Errorf("%w: chain %d", ErrUnknownSourceChain, deadChainID)
	}

	if !VerifyMerkleProof(accountLeafHash, proof, registry.AccountTreeRoot) {
		return ErrInvalidMerkleProof
	}

	if amount != nil && amount.Sign() > 0 && g.SupplyLedger != nil {
		if err := g.SupplyLedger.TransferAllocation(deadChainID, g.LocalChainID, amount); err != nil {
			return err
		}
	}

	g.DeadChainClaimed[claimKey] = true
	return nil
}

// GetOriginalSender provides recipient contracts with verified cross-chain origin metadata.
func (g *GatewayEngine) GetOriginalSender() (common.Address, uint64, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.ActiveContext == nil || !g.ActiveContext.IsGateway {
		return common.Address{}, 0, ErrNoActiveContext
	}
	return g.ActiveContext.OriginalSender, g.ActiveContext.SourceChainID, nil
}

// IsCalledByGateway verifies that the execution call originates from GatewayPrecompile.
func (g *GatewayEngine) IsCalledByGateway() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	return g.ActiveContext != nil && g.ActiveContext.IsGateway
}

// GetMessageStatus returns current lifecycle status for a given messageId.
func (g *GatewayEngine) GetMessageStatus(messageID common.Hash) MessageStatus {
	g.mu.RLock()
	defer g.mu.RUnlock()

	status, exists := g.MessageStatus[messageID]
	if !exists {
		return MessageStatusPending
	}
	return status
}
