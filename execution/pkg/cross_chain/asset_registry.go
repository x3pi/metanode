package cross_chain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

var (
	ErrAssetNotFound           = errors.New("asset not found in AssetRegistry")
	ErrAssetInactive           = errors.New("asset is currently inactive or paused")
	ErrAssetAlreadyExists      = errors.New("asset ID already registered")
	ErrInvalidCanonicalContract= errors.New("canonical contract address cannot be zero")
	ErrInvalidHomeChain        = errors.New("home chain ID is invalid or not registered")
	ErrInvalidWrappedContract  = errors.New("wrapped contract address cannot be zero")
	ErrUnauthorizedRegistration= errors.New("unauthorized asset registration attempt (must pass governance >= 2/3)")
	ErrInsufficientVaultBalance= errors.New("insufficient vault balance on home chain for unlock")
	ErrInsufficientCirculation = errors.New("insufficient wrapped token circulation on source chain for burn")
	ErrAssetSupplyMismatch     = errors.New("asset total supply does not match sum of vault and circulation balances")
	ErrZeroAssetAmount         = errors.New("asset amount must be greater than zero")
)

// AssetRegistryEngine manages cross-chain custom tokens (ERC-20 / Wrapped assets) (P6.1 & P6.2).
// Governed on Root Anchor via ProposalRegisterAsset (>= 2/3 active chains + 72h timelock).
type AssetRegistryEngine struct {
	mu                  sync.RWMutex
	Assets              map[string]*AssetEntry             // key: assetID.String()
	VaultBalances       map[string]*big.Int                // key: "assetID:chainID" -> locked tokens in home chain vault
	CirculationBalances map[string]*big.Int                // key: "assetID:chainID" -> wrapped tokens circulating on dest chains
	TotalSupplies       map[string]*big.Int                // key: assetID.String() -> canonical total supply
	ChainRegistry       map[uint64]ChainRegistry
	Governance          *GovernanceEngine
}

// NewAssetRegistryEngine creates a new multi-asset registry.
func NewAssetRegistryEngine(chainRegistry map[uint64]ChainRegistry, gov *GovernanceEngine) *AssetRegistryEngine {
	return &AssetRegistryEngine{
		Assets:              make(map[string]*AssetEntry),
		VaultBalances:       make(map[string]*big.Int),
		CirculationBalances: make(map[string]*big.Int),
		TotalSupplies:       make(map[string]*big.Int),
		ChainRegistry:       chainRegistry,
		Governance:          gov,
	}
}

// RegisterAssetOnRootAnchor registers a new token under AssetRegistry via Governance (P6.1).
func (a *AssetRegistryEngine) RegisterAssetOnRootAnchor(
	proposal *GovernanceProposal,
	totalSupply *big.Int,
) (*AssetEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if proposal == nil || proposal.Kind != ProposalRegisterAsset {
		return nil, ErrUnauthorizedRegistration
	}

	if !proposal.Executed {
		return nil, fmt.Errorf("%w: proposal %s has not been executed by governance", ErrUnauthorizedRegistration, proposal.ProposalID.Hex())
	}

	var entry AssetEntry
	if err := json.Unmarshal(proposal.Payload, &entry); err != nil {
		return nil, fmt.Errorf("invalid asset entry payload: %w", err)
	}

	if entry.AssetID == nil || entry.AssetID.Sign() <= 0 {
		return nil, fmt.Errorf("invalid asset ID: %v", entry.AssetID)
	}

	assetKey := entry.AssetID.String()
	if _, exists := a.Assets[assetKey]; exists {
		return nil, ErrAssetAlreadyExists
	}

	if entry.CanonicalContract == (common.Address{}) {
		return nil, ErrInvalidCanonicalContract
	}

	if a.ChainRegistry != nil {
		if _, exists := a.ChainRegistry[entry.HomeChainID]; !exists {
			return nil, fmt.Errorf("%w: home chain %d", ErrInvalidHomeChain, entry.HomeChainID)
		}
	}

	if totalSupply == nil || totalSupply.Sign() < 0 {
		return nil, fmt.Errorf("invalid total supply: %v", totalSupply)
	}

	entry.Active = true
	a.Assets[assetKey] = &entry
	a.TotalSupplies[assetKey] = new(big.Int).Set(totalSupply)

	// Initial circulation is 100% on Home Chain, 0 in vault, 0 on wrapped chains
	homeKey := fmt.Sprintf("%s:%d", assetKey, entry.HomeChainID)
	a.VaultBalances[homeKey] = big.NewInt(0)
	a.CirculationBalances[homeKey] = new(big.Int).Set(totalSupply)

	return &entry, nil
}

// GetAsset returns asset metadata by assetID.
func (a *AssetRegistryEngine) GetAsset(assetID *big.Int) (*AssetEntry, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if assetID == nil {
		return nil, ErrAssetNotFound
	}
	entry, exists := a.Assets[assetID.String()]
	if !exists {
		return nil, ErrAssetNotFound
	}
	if !entry.Active {
		return nil, ErrAssetInactive
	}
	return entry, nil
}

// LockAndBridgeAsset locks canonical tokens in the home chain vault and prepares outbound message (P6.2).
func (a *AssetRegistryEngine) LockAndBridgeAsset(
	sourceChainID uint64,
	destChainID uint64,
	sender common.Address,
	recipient common.Address,
	assetID *big.Int,
	amount *big.Int,
	tip *big.Int,
) (*CrossChainMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if amount == nil || amount.Sign() <= 0 {
		return nil, ErrZeroAssetAmount
	}

	assetKey := assetID.String()
	entry, exists := a.Assets[assetKey]
	if !exists {
		return nil, ErrAssetNotFound
	}
	if !entry.Active {
		return nil, ErrAssetInactive
	}

	wrappedDest, hasWrapped := entry.WrappedContracts[destChainID]
	if !hasWrapped && destChainID != entry.HomeChainID {
		return nil, fmt.Errorf("%w for destination chain %d", ErrInvalidWrappedContract, destChainID)
	}

	sourceCircKey := fmt.Sprintf("%s:%d", assetKey, sourceChainID)
	sourceCirc := a.CirculationBalances[sourceCircKey]
	if sourceCirc == nil || sourceCirc.Cmp(amount) < 0 {
		return nil, fmt.Errorf("%w: requested %s > available %s", ErrInsufficientCirculation, amount.String(), sourceCirc.String())
	}

	// 1. Deduct from source chain circulation
	a.CirculationBalances[sourceCircKey] = new(big.Int).Sub(sourceCirc, amount)

	// 2. If originating from Home Chain -> Lock into Vault
	if sourceChainID == entry.HomeChainID {
		vaultKey := fmt.Sprintf("%s:%d", assetKey, sourceChainID)
		currentVault := a.VaultBalances[vaultKey]
		if currentVault == nil {
			currentVault = big.NewInt(0)
		}
		a.VaultBalances[vaultKey] = new(big.Int).Add(currentVault, amount)
	}

	// 3. Construct cross-chain asset message
	targetContract := wrappedDest
	if destChainID == entry.HomeChainID {
		targetContract = entry.CanonicalContract
	}

	msg := &CrossChainMessage{
		MessageID:     Keccak256([]byte(fmt.Sprintf("%s:%s:%d:%d:%s", sender.Hex(), recipient.Hex(), sourceChainID, destChainID, amount.String()))),
		SourceChainID: sourceChainID,
		DestChainID:   destChainID,
		Sender:        sender,
		Target:        targetContract,
		Payload:       recipient.Bytes(),
		AssetID:       new(big.Int).Set(assetID),
		Value:         new(big.Int).Set(amount),
		Tip:           new(big.Int).Set(tip),
		HopCount:      1,
		Ordered:       false,
	}

	return msg, nil
}

// ReceiveAndSettleAsset mints wrapped tokens on destination chain or unlocks canonical tokens from vault (P6.2).
func (a *AssetRegistryEngine) ReceiveAndSettleAsset(
	destChainID uint64,
	recipient common.Address,
	assetID *big.Int,
	amount *big.Int,
) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if amount == nil || amount.Sign() <= 0 {
		return ErrZeroAssetAmount
	}

	assetKey := assetID.String()
	entry, exists := a.Assets[assetKey]
	if !exists {
		return ErrAssetNotFound
	}
	if !entry.Active {
		return ErrAssetInactive
	}

	destCircKey := fmt.Sprintf("%s:%d", assetKey, destChainID)
	currentCirc := a.CirculationBalances[destCircKey]
	if currentCirc == nil {
		currentCirc = big.NewInt(0)
	}

	// Case A: Destination is Home Chain -> Unlock from Vault back into circulation
	if destChainID == entry.HomeChainID {
		vaultKey := fmt.Sprintf("%s:%d", assetKey, destChainID)
		currentVault := a.VaultBalances[vaultKey]
		if currentVault == nil || currentVault.Cmp(amount) < 0 {
			return fmt.Errorf("%w: requested %s > vault %s", ErrInsufficientVaultBalance, amount.String(), currentVault.String())
		}
		a.VaultBalances[vaultKey] = new(big.Int).Sub(currentVault, amount)
		a.CirculationBalances[destCircKey] = new(big.Int).Add(currentCirc, amount)
		return nil
	}

	// Case B: Destination is Remote Chain -> Mint wrapped token into circulation
	a.CirculationBalances[destCircKey] = new(big.Int).Add(currentCirc, amount)
	return nil
}

// VerifyAssetConservationInvariant verifies that TotalSupply == VaultBalance + Sum(Circulation across all chains).
func (a *AssetRegistryEngine) VerifyAssetConservationInvariant(assetID *big.Int) (bool, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	assetKey := assetID.String()
	entry, exists := a.Assets[assetKey]
	if !exists {
		return false, ErrAssetNotFound
	}

	totalSupply := a.TotalSupplies[assetKey]
	if totalSupply == nil {
		return false, ErrAssetSupplyMismatch
	}

	// Compute total active circulating supply across all chains + vault
	sumCirculation := big.NewInt(0)
	for k, v := range a.CirculationBalances {
		var aID string
		fmt.Sscanf(k, "%s:", &aID)
		if len(k) > len(assetKey) && k[:len(assetKey)+1] == assetKey+":" {
			if v != nil {
				sumCirculation.Add(sumCirculation, v)
			}
		}
	}

	vaultKey := fmt.Sprintf("%s:%d", assetKey, entry.HomeChainID)
	vaultBal := a.VaultBalances[vaultKey]
	if vaultBal == nil {
		vaultBal = big.NewInt(0)
	}

	// Total canonical supply must equal sum of all circulating tokens on all chains
	if sumCirculation.Cmp(totalSupply) != 0 {
		return false, fmt.Errorf("%w: total supply %s != sum circulation %s (vault %s)",
			ErrAssetSupplyMismatch, totalSupply.String(), sumCirculation.String(), vaultBal.String())
	}

	return true, nil
}
