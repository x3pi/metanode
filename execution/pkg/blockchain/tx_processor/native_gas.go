package tx_processor

// nativeTransferGas prices a native transfer inside TrueBlockSTM.
//
//	intrinsic     gas every tx must cover (native transfer base cost, plus 25000 per EIP-7702 tuple)
//	newAccountGas anti-dust surcharge for creating the recipient account (EXE-03); mandatory
//	refund        EIP-7702 refund for authorities that already existed (capped at gas/5 like every EVM refund)
//	strict        true for EIP-7702 (SetCode) txs
//
// Plain transfers (strict == false) keep the long-standing rule the native fast path also applies: bill
// intrinsic + newAccountGas regardless of the signed gas limit, so the EXE-03 surcharge can never be waived by
// signing a low limit (a plain 21000-gas transfer to a new account is billed 45000 in BOTH pipelines).
//
// SetCode txs are new functionality with no compatibility baggage, so they are strict: the signed maxGas must
// pay for intrinsic gas (25000 per tuple, valid or not), the surcharge and everything else, otherwise the tx
// fails and the whole maxGas is billed (ok == false). When ok, gas <= maxGas: a sender never pays more than the
// gas limit it signed.
func nativeTransferGas(maxGas, intrinsic, newAccountGas, refund uint64, strict bool) (gas uint64, ok bool) {
	gas = intrinsic + newAccountGas
	if refund > 0 {
		if maxRefund := gas / 5; refund > maxRefund {
			refund = maxRefund
		}
		gas -= refund
	}
	if strict && gas > maxGas {
		return maxGas, false
	}
	return gas, true
}
