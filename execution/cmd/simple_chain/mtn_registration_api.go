package main

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
	"golang.org/x/time/rate"
)

// Account registration RPC (namespace "mtn"). With the account gate on, an address may only send transactions once the
// Parent Chain has registered it to THIS cluster. The user talks only to this node: it relays the registration to the
// Parent Chain automatically (rollup.RegistrationRelay) and the confirmed registration comes back as an ordered system
// event that sets the on-chain flag. A client signs the message returned by mtn_getRegistrationMessage with the
// address's secp256k1 key, calls mtn_registerAccount, then polls mtn_getRegistrationStatus.

var errRegistrationDisabled = errors.New("account registration is not enabled on this node (account_gate is not parent_registered)")

// ClusterIdentity is returned by mtn_getClusterIdentity.
type ClusterIdentity struct {
	ClusterKey    string `json:"clusterKey"`              // BLS public key of this cluster (hex)
	ChainID       uint64 `json:"chainId"`                 // chain ID of this execution chain
	AccountGate   bool   `json:"accountGate"`             // whether senders must be parent-registered
	MessageTag    string `json:"messageTag"`              // domain tag of the message to sign
	ConsensusMode string `json:"consensusMode,omitempty"` // "raft" or empty
	IsLeader      bool   `json:"isLeader"`                // true if this node is currently the master/leader
	Role          string `json:"role,omitempty"`          // "leader", "follower", or "disabled"
}

// RegistrationMessage is returned by mtn_getRegistrationMessage: sign HashToSign (raw secp256k1 signature over the
// 32-byte hash, no Ethereum prefix) with the address's key.
type RegistrationMessage struct {
	Digest     string `json:"digest"`
	HashToSign string `json:"hashToSign"`
}

// RegistrationInfo is returned by mtn_registerAccount and mtn_getRegistrationStatus.
type RegistrationInfo struct {
	Status      string `json:"status"`                // NONE | PENDING | CONFIRMED | REJECTED | FAILED
	HomeCluster string `json:"homeCluster,omitempty"` // the cluster that won (REJECTED only)
	Reason      string `json:"reason,omitempty"`
}

func (api *MtnAPI) registrationRelay() (*rollup.RegistrationRelay, error) {
	if api.App == nil || api.App.regRelay == nil || api.App.config == nil || !api.App.config.AccountGateParentRegistered() {
		return nil, errRegistrationDisabled
	}
	return api.App.regRelay, nil
}

func toRegistrationInfo(res rollup.RegistrationResult) *RegistrationInfo {
	info := &RegistrationInfo{Status: string(res.Status), Reason: res.Reason}
	if res.HomeCluster != nil {
		info.HomeCluster = "0x" + hex.EncodeToString(res.HomeCluster[:])
	}
	return info
}

// GetClusterIdentity returns the cluster key users register against.
func (api *MtnAPI) GetClusterIdentity() (*ClusterIdentity, error) {
	relay, err := api.registrationRelay()
	if err != nil {
		return nil, err
	}
	key := relay.ClusterKey()
	cid := &ClusterIdentity{
		ClusterKey:  "0x" + hex.EncodeToString(key[:]),
		ChainID:     api.App.config.ChainId.Uint64(),
		AccountGate: true,
		MessageTag:  string(parentchain.RegisterAccountDomainTag),
	}
	if raftfeed.Enabled() {
		cid.ConsensusMode = "raft"
		cid.IsLeader = raftfeed.IsLeader()
		cid.Role = raftfeed.Role()
	}
	return cid, nil
}

// GetRegistrationMessage returns the exact bytes a user must sign to register address with this cluster.
func (api *MtnAPI) GetRegistrationMessage(address common.Address) (*RegistrationMessage, error) {
	relay, err := api.registrationRelay()
	if err != nil {
		return nil, err
	}
	digest := relay.RegistrationDigest(address)
	return &RegistrationMessage{
		Digest:     hexutil.Encode(digest),
		HashToSign: hexutil.Encode(crypto.Keccak256(digest)),
	}, nil
}

// errRegistrationRateLimited is returned when one client IP submits registrations faster than allowed.
var errRegistrationRateLimited = errors.New("too many registration requests from this address, retry later")

const (
	defaultRegistrationRatePerMin = 20
	maxRegistrationLimiterIPs     = 10000
)

// registrationIPLimiter bounds mtn_registerAccount calls per client IP. Every accepted request costs an fsync, a slot in
// the relay's bounded table, a Parent Chain transaction and possibly a provisional flag on-chain, while new keys are
// free to generate; this keeps one client from filling the table and starving real users.
type registrationIPLimiter struct {
	mu     sync.Mutex
	perMin int
	byIP   map[string]*rate.Limiter
}

// newRegistrationIPLimiter reads REGISTRATION_RATE_PER_MIN (default 20; 0 disables the limit).
func newRegistrationIPLimiter() *registrationIPLimiter {
	perMin := defaultRegistrationRatePerMin
	if v := os.Getenv("REGISTRATION_RATE_PER_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			perMin = n
		}
	}
	return &registrationIPLimiter{perMin: perMin, byIP: make(map[string]*rate.Limiter)}
}

func isLoopbackAddr(ip string) bool {
	return ip == "" || ip == "127.0.0.1" || ip == "::1" || ip == "localhost"
}

func (l *registrationIPLimiter) allow(key string) bool {
	if l == nil || l.perMin <= 0 || key == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.byIP[key]
	if !ok {
		if len(l.byIP) >= maxRegistrationLimiterIPs {
			// Evict idle limiters whose tokens have completely replenished
			for k, v := range l.byIP {
				if v.Tokens() >= float64(l.perMin) {
					delete(l.byIP, k)
				}
			}
			// If still full, prune entries down to 3/4 capacity to maintain bounds
			if len(l.byIP) >= maxRegistrationLimiterIPs {
				for k := range l.byIP {
					delete(l.byIP, k)
					if len(l.byIP) < maxRegistrationLimiterIPs*3/4 {
						break
					}
				}
			}
		}
		lim = rate.NewLimiter(rate.Limit(float64(l.perMin)/60), l.perMin)
		l.byIP[key] = lim
	}
	return lim.Allow()
}

var registrationLimiter = newRegistrationIPLimiter()

// RegisterAccount accepts a registration request. It returns immediately with PENDING (or the current state if the
// address is already known); the relay and the Parent Chain finish the job automatically. The account is usable only
// once mtn_getRegistrationStatus reports PROVISIONAL (provisional flag committed on-chain) or CONFIRMED.
func (api *MtnAPI) RegisterAccount(ctx context.Context, address common.Address, userSig hexutil.Bytes) (*RegistrationInfo, error) {
	relay, err := api.registrationRelay()
	if err != nil {
		return nil, err
	}
	clientIP := getClientIPFromContext(ctx)
	limiterKey := "ip:" + clientIP
	if isLoopbackAddr(clientIP) {
		// When behind a reverse proxy (nginx_tls on loopback), rate limit per user address to avoid
		// pooling all clients into a single loopback IP budget.
		limiterKey = "addr:" + address.Hex()
	}
	if !registrationLimiter.allow(limiterKey) {
		return nil, errRegistrationRateLimited
	}
	res, err := relay.Submit(address, userSig)
	if err != nil {
		return nil, err
	}
	return toRegistrationInfo(res), nil
}

// GetRegistrationStatus reports where an address is in the registration flow. The on-chain flag always wins.
func (api *MtnAPI) GetRegistrationStatus(address common.Address) (*RegistrationInfo, error) {
	relay, err := api.registrationRelay()
	if err != nil {
		return nil, err
	}
	return toRegistrationInfo(relay.Status(address)), nil
}
